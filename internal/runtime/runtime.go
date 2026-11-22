// Package runtime is the single owner of live database connections,
// cancelable query goroutines, staged-edit drafts, safety gating, and the
// local audit log. It publishes immutable snapshots for internal/ui without
// importing mygo/ui.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AnalyseDeCircuit/kyandb/internal/config"
	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
	"github.com/AnalyseDeCircuit/kyandb/internal/sqlguard"
	"github.com/AnalyseDeCircuit/kyandb/internal/tunnel"
)

// TabKind selects how a workspace tab behaves.
type TabKind int

const (
	TabTable TabKind = iota
	TabQuery
)

// RowChange marks a row's staged state in a snapshot.
type RowChange int

const (
	RowUnchanged RowChange = iota
	RowModified
	RowInserted
	RowDeleted
)

// CellSnapshot is one cell in an immutable runtime snapshot.
type CellSnapshot struct {
	Text   string
	Null   bool
	Staged bool
}

// RowSnapshot is one row in an immutable runtime snapshot.
type RowSnapshot struct {
	Cells  []CellSnapshot
	Change RowChange
}

// PendingSnapshot summarizes uncommitted edits on a table tab.
type PendingSnapshot struct {
	Modified int
	Inserted int
	Deleted  int
}

// ConnectionSnapshot is the immutable view of one saved connection.
type ConnectionSnapshot struct {
	Profile   domain.Profile
	Version   string
	Connected bool
	Objects   []domain.SchemaObject
}

// TabSnapshot is the immutable view of one open workspace tab.
type TabSnapshot struct {
	ID        uint64
	ConnID    string
	Kind      TabKind
	Title     string
	Table     string
	Where     string
	Query     string
	Columns   []domain.Column
	Rows      []RowSnapshot
	Total     int64
	Elapsed   time.Duration
	Running   bool
	Error     string
	Pending   PendingSnapshot
	StagedSQL string
}

// Snapshot is an immutable point-in-time view of all workspace state safe to
// read on every UI frame without locking or I/O.
type Snapshot struct {
	Generation     uint64
	Connections    []ConnectionSnapshot
	Active         int // index into Connections, or -1
	Tabs           []TabSnapshot
	PendingHostKey *domain.HostKeyChallenge
}

// Credentials carries ephemeral passwords/passphrases from the UI layer into
// runtime without requiring internal/ui to import internal/secret.
type Credentials struct {
	Password      string
	SSHPassword   string
	SSHPassphrase string
}

type liveConn struct {
	conn    engine.Conn
	tun     *tunnel.Tunnel
	version string
	objects []domain.SchemaObject
}

type tabState struct {
	id        uint64
	connID    string
	kind      TabKind
	title     string
	table     string
	where     string
	query     string
	columns   []domain.Column
	baseRows  [][]domain.CellValue
	total     int64
	elapsed   time.Duration
	running   bool
	errText   string
	edits     []domain.StagedEdit
	stagedSQL string
	cancel    context.CancelFunc
	runSeq    uint64
}

// Runtime owns all KyanDB connections, SSH tunnels, async tasks, safety gates,
// and audit entries.
type Runtime struct {
	cfg      *config.Store
	secrets  secret.Store
	drivers  map[domain.Engine]engine.Driver
	hostKeys *tunnel.HostKeyStore
	tunnels  *tunnel.Manager

	mu             sync.Mutex
	closed         bool
	gen            uint64
	nextID         uint64
	profiles       []domain.Profile
	activeID       string
	conns          map[string]*liveConn
	tabs           []*tabState
	audit          []domain.AuditEntry
	pendingHostKey *domain.HostKeyChallenge
	pendingRetry   func()
	onChange       func()

	snap atomic.Pointer[Snapshot]
	wg   sync.WaitGroup
}

// New initializes the runtime, loads persisted non-sensitive profiles,
// initializes SSH host-key verification and tunnel management, and publishes
// the initial immutable snapshot.
func New(cfg *config.Store, secrets secret.Store, drivers ...engine.Driver) (*Runtime, error) {
	var hostKeys *tunnel.HostKeyStore
	if cfg != nil && cfg.Path() != "" {
		hostKeys = tunnel.NewHostKeyStore(filepath.Join(filepath.Dir(cfg.Path()), "known_hosts"))
	} else {
		hostKeys = tunnel.NewHostKeyStoreIsolated("")
	}
	return NewWithHostKeys(cfg, secrets, hostKeys, drivers...)
}

// NewWithHostKeys initializes the runtime with an explicit SSH HostKeyStore
// (used by tests to isolate from the developer's ~/.ssh/known_hosts).
func NewWithHostKeys(cfg *config.Store, secrets secret.Store, hostKeys *tunnel.HostKeyStore, drivers ...engine.Driver) (*Runtime, error) {
	if hostKeys == nil {
		hostKeys = tunnel.NewHostKeyStoreIsolated("")
	}
	mgr, err := tunnel.NewManager(hostKeys.Verify)
	if err != nil {
		return nil, err
	}
	drvMap := make(map[domain.Engine]engine.Driver, len(drivers))
	for _, d := range drivers {
		drvMap[d.Engine()] = d
	}
	var profiles []domain.Profile
	if cfg != nil {
		loaded, err := cfg.Load()
		if err != nil {
			_ = mgr.Close()
			return nil, err
		}
		profiles = loaded
	}
	rt := &Runtime{
		cfg:      cfg,
		secrets:  secrets,
		drivers:  drvMap,
		hostKeys: hostKeys,
		tunnels:  mgr,
		profiles: profiles,
		conns:    map[string]*liveConn{},
	}
	if len(profiles) > 0 {
		rt.activeID = profiles[0].ID
	}
	rt.publishLocked()
	return rt, nil
}

// SetOnChange registers a callback invoked (outside locks) whenever a new
// immutable snapshot is published.
func (r *Runtime) SetOnChange(fn func()) {
	r.mu.Lock()
	r.onChange = fn
	r.mu.Unlock()
}

// Snapshot returns the latest immutable workspace snapshot in O(1) without locking.
func (r *Runtime) Snapshot() Snapshot {
	if s := r.snap.Load(); s != nil {
		return *s
	}
	return Snapshot{Active: -1}
}

// AuditLog returns a copy of recorded audit entries.
func (r *Runtime) AuditLog() []domain.AuditEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.AuditEntry, len(r.audit))
	copy(out, r.audit)
	return out
}

// SaveProfile persists a new or updated connection profile, stores its
// password in the secret store if supplied, and selects it asynchronously.
func (r *Runtime) SaveProfile(profile domain.Profile, password secret.String) (domain.Profile, error) {
	return r.SaveProfileWithSSH(profile, password, secret.String{})
}

// SaveProfileWithSSH persists a profile and stores its DB password and SSH
// secret (password or private-key passphrase) under separate keychain entries.
func (r *Runtime) SaveProfileWithSSH(profile domain.Profile, password, sshSecret secret.String) (domain.Profile, error) {
	if strings.TrimSpace(profile.Name) == "" {
		return domain.Profile{}, fmt.Errorf("%w: empty name", domain.ErrInvalidProfile)
	}
	if profile.Env == "" {
		profile.Env = domain.EnvDev
	}
	if profile.ID == "" {
		r.mu.Lock()
		r.nextID++
		profile.ID = fmt.Sprintf("conn-%d-%d", time.Now().UnixNano(), r.nextID)
		r.mu.Unlock()
	}

	// Store credentials in OS keychain before writing non-sensitive metadata.
	if !password.Empty() && r.secrets != nil {
		ref := "kyandb:conn:" + profile.ID
		if err := r.secrets.Set(ref, []byte(password.Value())); err != nil {
			return domain.Profile{}, err
		}
		profile.SecretRef = ref
	}
	if !sshSecret.Empty() && r.secrets != nil {
		ref := "kyandb:ssh:" + profile.ID
		if err := r.secrets.Set(ref, []byte(sshSecret.Value())); err != nil {
			return domain.Profile{}, err
		}
		profile.SSHSecretRef = ref
	}

	r.mu.Lock()
	updated := false
	for i := range r.profiles {
		if r.profiles[i].ID == profile.ID {
			r.profiles[i] = profile
			updated = true
			break
		}
	}
	if !updated {
		r.profiles = append(r.profiles, profile)
	}
	r.activeID = profile.ID
	profilesCopy := append([]domain.Profile(nil), r.profiles...)
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()

	if r.cfg != nil {
		if err := r.cfg.Save(profilesCopy); err != nil {
			return domain.Profile{}, err
		}
	}
	if cb != nil {
		cb()
	}
	return profile, nil
}

// SaveAndConnect persists profile with ephemeral credentials and connects to
// it asynchronously on a runtime-owned worker.
func (r *Runtime) SaveAndConnect(profile domain.Profile, creds Credentials, onError func(error)) {
	dbPW := secret.NewString(creds.Password)
	sshSecret := selectSSHSecret(profile.SSHAuth, creds)
	r.launch(func() {
		saved, err := r.SaveProfileWithSSH(profile, dbPW, sshSecret)
		if err != nil {
			if onError != nil {
				onError(errors.New(secret.ScrubDSN(err.Error())))
			}
			return
		}
		r.connectWithChallengeRetry(saved.ID, onError)
	})
}

func (r *Runtime) connectWithChallengeRetry(connID string, onError func(error)) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := r.Connect(ctx, connID); err != nil {
		if errors.Is(err, domain.ErrHostKeyUnknown) || errors.Is(err, domain.ErrHostKeyMismatch) {
			r.stageHostKeyChallenge(func() {
				r.connectWithChallengeRetry(connID, onError)
			})
			return
		}
		if onError != nil {
			onError(errors.New(secret.ScrubDSN(err.Error())))
		}
	}
}

// TestConnectionAsync verifies connectivity on a runtime-owned worker without
// mutating saved profiles or active connections.
func (r *Runtime) TestConnectionAsync(profile domain.Profile, creds Credentials, onDone func(version string, elapsed time.Duration, err error)) {
	dbPW := secret.NewString(creds.Password)
	sshSecret := selectSSHSecret(profile.SSHAuth, creds)
	var run func()
	run = func() {
		r.launch(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ver, elapsed, err := r.TestConnectionWithSSH(ctx, profile, dbPW, sshSecret)
			if err != nil && (errors.Is(err, domain.ErrHostKeyUnknown) || errors.Is(err, domain.ErrHostKeyMismatch)) {
				r.stageHostKeyChallenge(run)
				return
			}
			if onDone != nil {
				onDone(ver, elapsed, err)
			}
		})
	}
	run()
}

// TestConnection verifies connectivity without mutating saved runtime state.
func (r *Runtime) TestConnection(ctx context.Context, profile domain.Profile, password secret.String) (string, time.Duration, error) {
	return r.TestConnectionWithSSH(ctx, profile, password, secret.String{})
}

// TestConnectionWithSSH verifies connectivity (including optional SSH/multi-hop
// tunnel) without mutating saved runtime state.
func (r *Runtime) TestConnectionWithSSH(ctx context.Context, profile domain.Profile, password, sshSecret secret.String) (string, time.Duration, error) {
	drv, ok := r.drivers[profile.Engine]
	if !ok {
		return "", 0, fmt.Errorf("%w: %s", domain.ErrUnsupportedEngine, profile.Engine)
	}
	start := time.Now()
	var dial engine.DialFunc
	if profile.SSHEnabled {
		tun, err := r.openTunnel(ctx, profile, sshSecret)
		if err != nil {
			return "", 0, err
		}
		defer tun.Close()
		dial = tun.DialContext
	}
	c, err := drv.Connect(ctx, profile, engine.ConnectOptions{Password: password, Dial: dial})
	if err != nil {
		return "", 0, errors.New(secret.ScrubDSN(err.Error()))
	}
	defer c.Close()

	ver, err := c.Version(ctx)
	if err != nil {
		return "", 0, errors.New(secret.ScrubDSN(err.Error()))
	}
	return ver, time.Since(start), nil
}

// Connect opens (or reuses) the connection for connID and refreshes its
// schema objects. All DB/SSH/keychain I/O runs outside the mutex.
func (r *Runtime) Connect(ctx context.Context, connID string) error {
	profile, drv, existing, err := r.prepareConnect(connID)
	if err != nil {
		return err
	}

	conn := existing
	var tun *tunnel.Tunnel
	if conn == nil {
		var pw secret.String
		if profile.SecretRef != "" && r.secrets != nil {
			b, err := r.secrets.Get(profile.SecretRef)
			if err == nil {
				pw = secret.NewString(string(b.Raw()))
				b.Zero()
			} else if !errors.Is(err, domain.ErrSecretNotFound) {
				return err
			}
		}
		var dial engine.DialFunc
		if profile.SSHEnabled {
			tun, err = r.openTunnel(ctx, profile, secret.String{})
			if err != nil {
				return err
			}
			dial = tun.DialContext
		}
		conn, err = drv.Connect(ctx, profile, engine.ConnectOptions{Password: pw, Dial: dial})
		if err != nil {
			if tun != nil {
				_ = tun.Close()
			}
			return errors.New(secret.ScrubDSN(err.Error()))
		}
	}

	cleanupNew := func() {
		if existing == nil {
			_ = conn.Close()
			if tun != nil {
				_ = tun.Close()
			}
		}
	}

	ver, err := conn.Version(ctx)
	if err != nil {
		cleanupNew()
		return errors.New(secret.ScrubDSN(err.Error()))
	}
	objs, err := conn.ListObjects(ctx)
	if err != nil {
		cleanupNew()
		return errors.New(secret.ScrubDSN(err.Error()))
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cleanupNew()
		return context.Canceled
	}
	if prev, ok := r.conns[connID]; ok && existing == nil {
		// Another goroutine won the connect race; keep the existing owner.
		prev.version = ver
		prev.objects = objs
		r.activeID = connID
		r.publishLocked()
		cb := r.onChange
		r.mu.Unlock()
		cleanupNew()
		if cb != nil {
			cb()
		}
		return nil
	}
	if existing != nil {
		if prev := r.conns[connID]; prev != nil {
			prev.version = ver
			prev.objects = objs
		}
	} else {
		r.conns[connID] = &liveConn{conn: conn, tun: tun, version: ver, objects: objs}
	}
	r.activeID = connID
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()

	if cb != nil {
		cb()
	}
	return nil
}

// TrustHostKey approves the currently pending SSH host key challenge (for this
// session only when save is false, or persisted to KyanDB's known_hosts when
// save is true) and automatically retries the paused connection/test.
func (r *Runtime) TrustHostKey(save bool) error {
	if _, err := r.hostKeys.TrustPending(save); err != nil {
		return err
	}
	r.mu.Lock()
	retry := r.pendingRetry
	r.pendingHostKey = nil
	r.pendingRetry = nil
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
	if retry != nil {
		r.launch(retry)
	}
	return nil
}

// RejectHostKey declines the pending SSH host key challenge without trusting it.
func (r *Runtime) RejectHostKey() {
	r.hostKeys.DiscardPending()
	r.mu.Lock()
	r.pendingHostKey = nil
	r.pendingRetry = nil
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (r *Runtime) stageHostKeyChallenge(retry func()) {
	ch, ok := r.hostKeys.PendingChallenge()
	if !ok {
		return
	}
	r.mu.Lock()
	chCopy := ch
	r.pendingHostKey = &chCopy
	r.pendingRetry = retry
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (r *Runtime) openTunnel(ctx context.Context, profile domain.Profile, inlineSecret secret.String) (*tunnel.Tunnel, error) {
	auth, err := r.resolveSSHAuth(profile, inlineSecret)
	if err != nil {
		return nil, err
	}
	chain, err := tunnel.ChainFromProfile(profile, auth)
	if err != nil {
		return nil, err
	}
	tun, err := r.tunnels.Open(ctx, chain)
	if err != nil {
		if errors.Is(err, tunnel.ErrHostKeyRejected) {
			if ch, ok := r.hostKeys.PendingChallenge(); ok {
				if ch.Mismatch {
					return nil, domain.ErrHostKeyMismatch
				}
				return nil, domain.ErrHostKeyUnknown
			}
		}
		return nil, errors.New(secret.ScrubDSN(err.Error()))
	}
	return tun, nil
}

func (r *Runtime) resolveSSHAuth(profile domain.Profile, inlineSecret secret.String) (tunnel.Auth, error) {
	loadStored := func() (*secret.Bytes, error) {
		if !inlineSecret.Empty() {
			return secret.NewBytes([]byte(inlineSecret.Value())), nil
		}
		if profile.SSHSecretRef == "" || r.secrets == nil {
			return nil, nil
		}
		b, err := r.secrets.Get(profile.SSHSecretRef)
		if errors.Is(err, domain.ErrSecretNotFound) {
			return nil, nil
		}
		return b, err
	}

	switch profile.SSHAuth {
	case domain.SSHAuthPassword:
		b, err := loadStored()
		if err != nil {
			return tunnel.Auth{}, err
		}
		if b == nil || len(b.Raw()) == 0 {
			return tunnel.Auth{}, fmt.Errorf("%w: missing SSH password", domain.ErrInvalidProfile)
		}
		pw := secret.NewString(string(b.Raw()))
		b.Zero()
		return tunnel.PasswordAuth(pw), nil

	case domain.SSHAuthKey:
		path, err := expandHomePath(profile.SSHKeyPath)
		if err != nil || path == "" {
			return tunnel.Auth{}, fmt.Errorf("%w: missing SSH private key path", domain.ErrInvalidProfile)
		}
		passBytes, err := loadStored()
		if err != nil {
			return tunnel.Auth{}, err
		}
		// KeyFileAuth always zeroes passBytes before returning.
		return tunnel.KeyFileAuth(path, passBytes)

	default:
		return tunnel.AgentAuth(), nil
	}
}

func selectSSHSecret(auth domain.SSHAuthKind, creds Credentials) secret.String {
	switch auth {
	case domain.SSHAuthPassword:
		return secret.NewString(creds.SSHPassword)
	case domain.SSHAuthKey:
		return secret.NewString(creds.SSHPassphrase)
	default:
		return secret.String{}
	}
}

func expandHomePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

func (r *Runtime) prepareConnect(connID string) (domain.Profile, engine.Driver, engine.Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var profile domain.Profile
	found := false
	for _, p := range r.profiles {
		if p.ID == connID {
			profile = p
			found = true
			break
		}
	}
	if !found {
		return domain.Profile{}, nil, nil, fmt.Errorf("%w: %s", domain.ErrNotConnected, connID)
	}
	drv, ok := r.drivers[profile.Engine]
	if !ok {
		return domain.Profile{}, nil, nil, fmt.Errorf("%w: %s", domain.ErrUnsupportedEngine, profile.Engine)
	}
	var existing engine.Conn
	if lc := r.conns[connID]; lc != nil {
		existing = lc.conn
	}
	return profile, drv, existing, nil
}

// SelectConnection switches the active connection and asynchronously connects
// or refreshes its schema.
func (r *Runtime) SelectConnection(index int) {
	r.mu.Lock()
	if index < 0 || index >= len(r.profiles) || r.closed {
		r.mu.Unlock()
		return
	}
	connID := r.profiles[index].ID
	r.activeID = connID
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}

	r.launch(func() {
		r.connectWithChallengeRetry(connID, nil)
	})
}

// Refresh reloads schema objects for the active connection and re-runs any
// open table tabs bound to that connection.
func (r *Runtime) Refresh() {
	r.mu.Lock()
	connID := r.activeID
	var reloadIdx []int
	var reloadWhere []string
	for i, t := range r.tabs {
		if t.connID == connID && t.kind == TabTable && len(t.edits) == 0 {
			reloadIdx = append(reloadIdx, i)
			reloadWhere = append(reloadWhere, t.where)
		}
	}
	r.mu.Unlock()
	if connID == "" {
		return
	}
	r.launch(func() {
		r.connectWithChallengeRetry(connID, nil)
	})
	for i, idx := range reloadIdx {
		r.ReloadTable(idx, reloadWhere[i])
	}
}

// OpenTable opens (or focuses) a table browser tab for the active connection
// and loads its rows asynchronously. Returns the tab index.
func (r *Runtime) OpenTable(table string) int {
	r.mu.Lock()
	connID := r.activeID
	if connID == "" || r.closed {
		r.mu.Unlock()
		return -1
	}
	for i, t := range r.tabs {
		if t.connID == connID && t.kind == TabTable && t.table == table {
			r.mu.Unlock()
			r.ReloadTable(i, t.where)
			return i
		}
	}
	r.nextID++
	tab := &tabState{
		id:      r.nextID,
		connID:  connID,
		kind:    TabTable,
		title:   table,
		table:   table,
		running: true,
	}
	r.tabs = append(r.tabs, tab)
	idx := len(r.tabs) - 1
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}

	r.ReloadTable(idx, "")
	return idx
}

// ReloadTable fetches rows for a table tab using the supplied WHERE clause.
func (r *Runtime) ReloadTable(tabIdx int, where string) {
	r.mu.Lock()
	if tabIdx < 0 || tabIdx >= len(r.tabs) || r.closed {
		r.mu.Unlock()
		return
	}
	tab := r.tabs[tabIdx]
	if tab.kind != TabTable {
		r.mu.Unlock()
		return
	}
	if tab.cancel != nil {
		tab.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	tab.cancel = cancel
	tab.runSeq++
	seq := tab.runSeq
	tab.where = where
	tab.running = true
	tab.errText = ""
	connID, table := tab.connID, tab.table
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}

	r.launch(func() {
		defer cancel()
		if err := r.ensureConnected(ctx, connID); err != nil {
			r.finishTabError(tab.id, seq, err)
			return
		}
		conn := r.liveConnFor(connID)
		if conn == nil {
			r.finishTabError(tab.id, seq, domain.ErrNotConnected)
			return
		}
		res, err := conn.BrowseTable(ctx, table, where, 200, 0)
		if err != nil {
			r.finishTabError(tab.id, seq, err)
			return
		}
		r.finishTabResult(tab.id, seq, res, false)
	})
}

// NewQueryTab opens a fresh SQL editor tab bound to the active connection.
func (r *Runtime) NewQueryTab(initialSQL string) int {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return -1
	}
	r.nextID++
	n := 1
	for _, t := range r.tabs {
		if t.kind == TabQuery {
			n++
		}
	}
	tab := &tabState{
		id:     r.nextID,
		connID: r.activeID,
		kind:   TabQuery,
		title:  fmt.Sprintf("query %d", n),
		query:  initialSQL,
	}
	r.tabs = append(r.tabs, tab)
	idx := len(r.tabs) - 1
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
	return idx
}

// CloseTab cancels any in-flight query on the tab and removes it without
// closing the shared database connection.
func (r *Runtime) CloseTab(tabIdx int) {
	r.mu.Lock()
	if tabIdx < 0 || tabIdx >= len(r.tabs) {
		r.mu.Unlock()
		return
	}
	tab := r.tabs[tabIdx]
	if tab.cancel != nil {
		tab.cancel()
		tab.cancel = nil
	}
	r.tabs = append(r.tabs[:tabIdx], r.tabs[tabIdx+1:]...)
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// CancelQuery cancels the active in-flight query on tabIdx.
func (r *Runtime) CancelQuery(tabIdx int) {
	r.mu.Lock()
	if tabIdx < 0 || tabIdx >= len(r.tabs) {
		r.mu.Unlock()
		return
	}
	tab := r.tabs[tabIdx]
	if tab.cancel != nil {
		tab.cancel()
		tab.cancel = nil
	}
	tab.running = false
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// RunQuery enforces the runtime SafetyGate and executes sql asynchronously.
func (r *Runtime) RunQuery(tabIdx int, sqlText string) {
	r.mu.Lock()
	if tabIdx < 0 || tabIdx >= len(r.tabs) || r.closed {
		r.mu.Unlock()
		return
	}
	tab := r.tabs[tabIdx]
	tab.query = sqlText
	profile, ok := r.profileLocked(tab.connID)
	if !ok {
		tab.errText = domain.ErrNotConnected.Error()
		r.publishLocked()
		cb := r.onChange
		r.mu.Unlock()
		if cb != nil {
			cb()
		}
		return
	}

	// Central runtime SafetyGate: never rely solely on UI buttons.
	cls := sqlguard.Classify(sqlText)
	if profile.ReadOnly && cls.Mutates() {
		tab.errText = domain.ErrReadOnly.Error()
		r.recordAuditLocked(profile, "user", summarizeSQL(sqlText), 0, "blocked")
		r.publishLocked()
		cb := r.onChange
		r.mu.Unlock()
		if cb != nil {
			cb()
		}
		return
	}

	if tab.cancel != nil {
		tab.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	tab.cancel = cancel
	tab.runSeq++
	seq := tab.runSeq
	tab.running = true
	tab.errText = ""
	connID := tab.connID
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}

	r.launch(func() {
		defer cancel()
		if err := r.ensureConnected(ctx, connID); err != nil {
			r.finishTabError(tab.id, seq, err)
			return
		}
		conn := r.liveConnFor(connID)
		if conn == nil {
			r.finishTabError(tab.id, seq, domain.ErrNotConnected)
			return
		}
		res, err := conn.ExecQuery(ctx, sqlText)
		if err != nil {
			status := "error"
			if errors.Is(err, context.Canceled) {
				status = "canceled"
			}
			r.recordAudit(profile, "user", summarizeSQL(sqlText), 0, status)
			r.finishTabError(tab.id, seq, err)
			return
		}
		r.recordAudit(profile, "user", summarizeSQL(sqlText), res.Affected, "ok")
		r.finishTabResult(tab.id, seq, res, cls.Mutates())
		if cls.Mutates() {
			// Refresh schema list after DDL/DML mutations in the background.
			_ = r.Connect(context.Background(), connID)
		}
	})
}

// ExplainQuery executes a read-only query plan for sqlText without overwriting
// the user's editor text in the snapshot.
func (r *Runtime) ExplainQuery(tabIdx int, sqlText string) {
	r.mu.Lock()
	if tabIdx < 0 || tabIdx >= len(r.tabs) || r.closed {
		r.mu.Unlock()
		return
	}
	tab := r.tabs[tabIdx]
	tab.query = sqlText
	profile, ok := r.profileLocked(tab.connID)
	if !ok {
		tab.errText = domain.ErrNotConnected.Error()
		r.publishLocked()
		cb := r.onChange
		r.mu.Unlock()
		if cb != nil {
			cb()
		}
		return
	}
	trimmed := strings.TrimSpace(sqlText)
	if trimmed == "" {
		tab.errText = domain.ErrEmptySQL.Error()
		r.publishLocked()
		cb := r.onChange
		r.mu.Unlock()
		if cb != nil {
			cb()
		}
		return
	}
	explainSQL := trimmed
	if !strings.HasPrefix(strings.ToUpper(trimmed), "EXPLAIN") {
		if profile.Engine == domain.EngineSQLite {
			explainSQL = "EXPLAIN QUERY PLAN " + trimmed
		} else {
			explainSQL = "EXPLAIN " + trimmed
		}
	}
	if profile.ReadOnly && sqlguard.Classify(explainSQL).Mutates() {
		tab.errText = domain.ErrReadOnly.Error()
		r.recordAuditLocked(profile, "user", summarizeSQL(explainSQL), 0, "blocked")
		r.publishLocked()
		cb := r.onChange
		r.mu.Unlock()
		if cb != nil {
			cb()
		}
		return
	}
	if tab.cancel != nil {
		tab.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	tab.cancel = cancel
	tab.runSeq++
	seq := tab.runSeq
	tab.running = true
	tab.errText = ""
	connID := tab.connID
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}

	r.launch(func() {
		defer cancel()
		if err := r.ensureConnected(ctx, connID); err != nil {
			r.finishTabError(tab.id, seq, err)
			return
		}
		conn := r.liveConnFor(connID)
		if conn == nil {
			r.finishTabError(tab.id, seq, domain.ErrNotConnected)
			return
		}
		res, err := conn.ExecQuery(ctx, explainSQL)
		if err != nil {
			r.finishTabError(tab.id, seq, err)
			return
		}
		r.finishTabResult(tab.id, seq, res, false)
	})
}

// DismissError clears the inline error banner on tabIdx.
func (r *Runtime) DismissError(tabIdx int) {
	r.mu.Lock()
	if tabIdx < 0 || tabIdx >= len(r.tabs) {
		r.mu.Unlock()
		return
	}
	r.tabs[tabIdx].errText = ""
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// StageCellEdit records a local draft update for one cell in a table tab and
// regenerates its Review SQL preview.
func (r *Runtime) StageCellEdit(tabIdx, rowIdx, colIdx int, val domain.CellValue) error {
	r.mu.Lock()
	defer func() {
		cb := r.onChange
		r.mu.Unlock()
		if cb != nil {
			cb()
		}
	}()

	if tabIdx < 0 || tabIdx >= len(r.tabs) {
		return fmt.Errorf("invalid tab index %d", tabIdx)
	}
	tab := r.tabs[tabIdx]
	if tab.kind != TabTable || rowIdx < 0 || rowIdx >= len(tab.baseRows) || colIdx < 0 || colIdx >= len(tab.columns) {
		return fmt.Errorf("invalid cell coordinate (%d,%d)", rowIdx, colIdx)
	}
	profile, ok := r.profileLocked(tab.connID)
	if !ok {
		return domain.ErrNotConnected
	}
	if profile.ReadOnly {
		tab.errText = domain.ErrReadOnly.Error()
		r.publishLocked()
		return domain.ErrReadOnly
	}

	keys := extractRowKeys(tab.columns, tab.baseRows[rowIdx])
	if len(keys) == 0 {
		tab.errText = domain.ErrMissingPrimaryKey.Error()
		r.publishLocked()
		return domain.ErrMissingPrimaryKey
	}

	colName := tab.columns[colIdx].Name
	merged := false
	for i := range tab.edits {
		if tab.edits[i].Kind == domain.EditUpdate && tab.edits[i].RowIdx == rowIdx {
			replaced := false
			for j := range tab.edits[i].Values {
				if tab.edits[i].Values[j].Column == colName {
					tab.edits[i].Values[j].Value = val
					replaced = true
					break
				}
			}
			if !replaced {
				tab.edits[i].Values = append(tab.edits[i].Values, domain.ColumnValue{Column: colName, Value: val})
			}
			merged = true
			break
		}
	}
	if !merged {
		tab.edits = append(tab.edits, domain.StagedEdit{
			Kind:   domain.EditUpdate,
			RowIdx: rowIdx,
			Keys:   keys,
			Values: []domain.ColumnValue{{Column: colName, Value: val}},
		})
	}

	_, preview, err := sqlguard.BuildStagedSQL(profile.Engine, tab.table, tab.edits)
	if err != nil {
		return err
	}
	tab.stagedSQL = preview
	tab.errText = ""
	r.publishLocked()
	return nil
}

// StageDeleteRow marks one row for deletion in the local draft.
func (r *Runtime) StageDeleteRow(tabIdx, rowIdx int) error {
	r.mu.Lock()
	defer func() {
		cb := r.onChange
		r.mu.Unlock()
		if cb != nil {
			cb()
		}
	}()

	if tabIdx < 0 || tabIdx >= len(r.tabs) {
		return fmt.Errorf("invalid tab index %d", tabIdx)
	}
	tab := r.tabs[tabIdx]
	if tab.kind != TabTable || rowIdx < 0 || rowIdx >= len(tab.baseRows) {
		return fmt.Errorf("invalid row index %d", rowIdx)
	}
	profile, ok := r.profileLocked(tab.connID)
	if !ok {
		return domain.ErrNotConnected
	}
	if profile.ReadOnly {
		tab.errText = domain.ErrReadOnly.Error()
		r.publishLocked()
		return domain.ErrReadOnly
	}
	keys := extractRowKeys(tab.columns, tab.baseRows[rowIdx])
	if len(keys) == 0 {
		tab.errText = domain.ErrMissingPrimaryKey.Error()
		r.publishLocked()
		return domain.ErrMissingPrimaryKey
	}

	// Replace any existing edit on the same row with the delete.
	filtered := tab.edits[:0]
	for _, e := range tab.edits {
		if e.RowIdx != rowIdx {
			filtered = append(filtered, e)
		}
	}
	tab.edits = append(filtered, domain.StagedEdit{
		Kind:   domain.EditDelete,
		RowIdx: rowIdx,
		Keys:   keys,
	})
	_, preview, err := sqlguard.BuildStagedSQL(profile.Engine, tab.table, tab.edits)
	if err != nil {
		return err
	}
	tab.stagedSQL = preview
	tab.errText = ""
	r.publishLocked()
	return nil
}

// DiscardStaged drops all uncommitted staged edits on tabIdx.
func (r *Runtime) DiscardStaged(tabIdx int) {
	r.mu.Lock()
	if tabIdx < 0 || tabIdx >= len(r.tabs) {
		r.mu.Unlock()
		return
	}
	tab := r.tabs[tabIdx]
	tab.edits = nil
	tab.stagedSQL = ""
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// CommitStaged validates read-only locks, builds parameterized statements via
// sqlguard, executes them inside one database transaction, logs the audit
// entry, and reloads the table on success.
func (r *Runtime) CommitStaged(ctx context.Context, tabIdx int) error {
	r.mu.Lock()
	if tabIdx < 0 || tabIdx >= len(r.tabs) {
		r.mu.Unlock()
		return fmt.Errorf("invalid tab index %d", tabIdx)
	}
	tab := r.tabs[tabIdx]
	profile, ok := r.profileLocked(tab.connID)
	if !ok {
		r.mu.Unlock()
		return domain.ErrNotConnected
	}
	if profile.ReadOnly {
		tab.errText = domain.ErrReadOnly.Error()
		r.recordAuditLocked(profile, "user", "COMMIT staged "+tab.table, 0, "blocked")
		r.publishLocked()
		cb := r.onChange
		r.mu.Unlock()
		if cb != nil {
			cb()
		}
		return domain.ErrReadOnly
	}
	editsCopy := append([]domain.StagedEdit(nil), tab.edits...)
	table := tab.table
	where := tab.where
	connID := tab.connID
	tabID := tab.id
	r.mu.Unlock()

	if len(editsCopy) == 0 {
		return nil
	}
	stmts, _, err := sqlguard.BuildStagedSQL(profile.Engine, table, editsCopy)
	if err != nil {
		return err
	}
	if err := r.ensureConnected(ctx, connID); err != nil {
		return err
	}
	conn := r.liveConnFor(connID)
	if conn == nil {
		return domain.ErrNotConnected
	}

	affected, err := conn.ExecTransaction(ctx, stmts)
	if err != nil {
		cleanErr := errors.New(secret.ScrubDSN(err.Error()))
		r.recordAudit(profile, "user", fmt.Sprintf("COMMIT %d staged edits on %s", len(editsCopy), table), 0, "error")
		r.mu.Lock()
		if t := r.findTabLocked(tabID); t != nil {
			t.errText = cleanErr.Error()
			r.publishLocked()
		}
		cb := r.onChange
		r.mu.Unlock()
		if cb != nil {
			cb()
		}
		return cleanErr
	}

	r.recordAudit(profile, "user", fmt.Sprintf("COMMIT %d staged edits on %s", len(editsCopy), table), affected, "ok")

	res, browseErr := conn.BrowseTable(ctx, table, where, 200, 0)
	r.mu.Lock()
	if t := r.findTabLocked(tabID); t != nil {
		t.edits = nil
		t.stagedSQL = ""
		t.errText = ""
		if browseErr == nil {
			t.columns = res.Columns
			t.baseRows = res.Rows
			t.total = res.Total
			t.elapsed = res.Elapsed
		}
		r.publishLocked()
	}
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
	return nil
}

// CommitStagedAsync runs CommitStaged on a runtime-owned worker so the UI
// thread never blocks on database I/O.
func (r *Runtime) CommitStagedAsync(tabIdx int) {
	r.launch(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = r.CommitStaged(ctx, tabIdx)
	})
}

// Close deterministically cancels every in-flight query, waits for worker
// goroutines to exit, and closes all open database pools.
func (r *Runtime) Close() error {
	r.mu.Lock()
	r.closed = true
	for _, tab := range r.tabs {
		if tab.cancel != nil {
			tab.cancel()
			tab.cancel = nil
		}
	}
	r.mu.Unlock()

	r.wg.Wait()

	r.mu.Lock()
	var firstErr error
	for id, lc := range r.conns {
		if err := lc.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if lc.tun != nil {
			if err := lc.tun.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		delete(r.conns, id)
	}
	r.mu.Unlock()
	if r.tunnels != nil {
		if err := r.tunnels.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// WaitIdle blocks until all background runtime goroutines finish; useful for
// deterministic tests without sleeps.
func (r *Runtime) WaitIdle() { r.wg.Wait() }

func (r *Runtime) launch(fn func()) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		fn()
	}()
}

func (r *Runtime) ensureConnected(ctx context.Context, connID string) error {
	if r.liveConnFor(connID) != nil {
		return nil
	}
	return r.Connect(ctx, connID)
}

func (r *Runtime) liveConnFor(connID string) engine.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lc := r.conns[connID]; lc != nil {
		return lc.conn
	}
	return nil
}

func (r *Runtime) profileLocked(connID string) (domain.Profile, bool) {
	for _, p := range r.profiles {
		if p.ID == connID {
			return p, true
		}
	}
	return domain.Profile{}, false
}

func (r *Runtime) findTabLocked(tabID uint64) *tabState {
	for _, t := range r.tabs {
		if t.id == tabID {
			return t
		}
	}
	return nil
}

func (r *Runtime) finishTabError(tabID, seq uint64, err error) {
	r.mu.Lock()
	tab := r.findTabLocked(tabID)
	if tab == nil || tab.runSeq != seq {
		r.mu.Unlock()
		return
	}
	tab.running = false
	tab.cancel = nil
	if !errors.Is(err, context.Canceled) {
		tab.errText = secret.ScrubDSN(err.Error())
	}
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (r *Runtime) finishTabResult(tabID, seq uint64, res domain.QueryResult, clearEdits bool) {
	r.mu.Lock()
	tab := r.findTabLocked(tabID)
	if tab == nil || tab.runSeq != seq {
		r.mu.Unlock()
		return
	}
	tab.running = false
	tab.cancel = nil
	tab.errText = ""
	tab.columns = res.Columns
	tab.baseRows = res.Rows
	tab.total = res.Total
	tab.elapsed = res.Elapsed
	if clearEdits {
		tab.edits = nil
		tab.stagedSQL = ""
	}
	r.publishLocked()
	cb := r.onChange
	r.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (r *Runtime) recordAudit(p domain.Profile, actor, summary string, affected int64, status string) {
	r.mu.Lock()
	r.recordAuditLocked(p, actor, summary, affected, status)
	r.mu.Unlock()
}

func (r *Runtime) recordAuditLocked(p domain.Profile, actor, summary string, affected int64, status string) {
	r.audit = append(r.audit, domain.AuditEntry{
		Timestamp:    time.Now(),
		ConnectionID: p.ID,
		Env:          p.Env,
		Actor:        actor,
		Summary:      secret.ScrubDSN(summary),
		Affected:     affected,
		Status:       status,
	})
}

func (r *Runtime) publishLocked() {
	r.gen++
	conns := make([]ConnectionSnapshot, len(r.profiles))
	activeIdx := -1
	for i, p := range r.profiles {
		if p.ID == r.activeID {
			activeIdx = i
		}
		cs := ConnectionSnapshot{Profile: p}
		if lc := r.conns[p.ID]; lc != nil {
			cs.Connected = true
			cs.Version = lc.version
			cs.Objects = append([]domain.SchemaObject(nil), lc.objects...)
		}
		conns[i] = cs
	}

	tabs := make([]TabSnapshot, len(r.tabs))
	for i, t := range r.tabs {
		rows, pend := overlayEdits(t.columns, t.baseRows, t.edits)
		tabs[i] = TabSnapshot{
			ID:        t.id,
			ConnID:    t.connID,
			Kind:      t.kind,
			Title:     t.title,
			Table:     t.table,
			Where:     t.where,
			Query:     t.query,
			Columns:   append([]domain.Column(nil), t.columns...),
			Rows:      rows,
			Total:     t.total,
			Elapsed:   t.elapsed,
			Running:   t.running,
			Error:     t.errText,
			Pending:   pend,
			StagedSQL: t.stagedSQL,
		}
	}

	var hk *domain.HostKeyChallenge
	if r.pendingHostKey != nil {
		cp := *r.pendingHostKey
		hk = &cp
	}
	s := &Snapshot{
		Generation:     r.gen,
		Connections:    conns,
		Active:         activeIdx,
		Tabs:           tabs,
		PendingHostKey: hk,
	}
	r.snap.Store(s)
}

func extractRowKeys(cols []domain.Column, row []domain.CellValue) []domain.ColumnValue {
	var keys []domain.ColumnValue
	for i, col := range cols {
		if col.PrimaryKey && i < len(row) {
			keys = append(keys, domain.ColumnValue{Column: col.Name, Value: row[i]})
		}
	}
	return keys
}

func overlayEdits(cols []domain.Column, base [][]domain.CellValue, edits []domain.StagedEdit) ([]RowSnapshot, PendingSnapshot) {
	colIdxByName := make(map[string]int, len(cols))
	for i, c := range cols {
		colIdxByName[c.Name] = i
	}
	rows := make([]RowSnapshot, len(base))
	for i, br := range base {
		cells := make([]CellSnapshot, len(br))
		for j, cv := range br {
			cells[j] = CellSnapshot{Text: cv.Text, Null: cv.Null}
		}
		rows[i] = RowSnapshot{Cells: cells}
	}
	var pend PendingSnapshot
	for _, e := range edits {
		switch e.Kind {
		case domain.EditUpdate:
			if e.RowIdx >= 0 && e.RowIdx < len(rows) {
				rows[e.RowIdx].Change = RowModified
				for _, cv := range e.Values {
					if ci, ok := colIdxByName[cv.Column]; ok && ci < len(rows[e.RowIdx].Cells) {
						rows[e.RowIdx].Cells[ci] = CellSnapshot{Text: cv.Value.Text, Null: cv.Value.Null, Staged: true}
					}
				}
				pend.Modified++
			}
		case domain.EditDelete:
			if e.RowIdx >= 0 && e.RowIdx < len(rows) {
				rows[e.RowIdx].Change = RowDeleted
				pend.Deleted++
			}
		case domain.EditInsert:
			cells := make([]CellSnapshot, len(cols))
			for j := range cells {
				cells[j] = CellSnapshot{Null: true}
			}
			for _, cv := range e.Values {
				if ci, ok := colIdxByName[cv.Column]; ok && ci < len(cells) {
					cells[ci] = CellSnapshot{Text: cv.Value.Text, Null: cv.Value.Null, Staged: true}
				}
			}
			rows = append(rows, RowSnapshot{Cells: cells, Change: RowInserted})
			pend.Inserted++
		}
	}
	return rows, pend
}

func summarizeSQL(sql string) string {
	s := strings.Join(strings.Fields(strings.TrimSpace(sql)), " ")
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
