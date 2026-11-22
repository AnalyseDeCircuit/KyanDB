package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/AnalyseDeCircuit/kyandb/internal/config"
	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
	"github.com/AnalyseDeCircuit/kyandb/internal/tunnel"
)

func startTestSSHServer(t *testing.T, expectedUser, expectedPass string) (host string, port int, pubKey ssh.PublicKey, forwards *atomic.Int32) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	fwdCount := &atomic.Int32{}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if meta.User() == expectedUser && string(pass) == expectedPass {
				return nil, nil
			}
			return nil, errors.New("rejected")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				stopRaw := context.AfterFunc(ctx, func() { _ = raw.Close() })
				defer stopRaw()
				defer raw.Close()
				sc, chans, reqs, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for req := range chans {
					if req.ChannelType() != "direct-tcpip" {
						req.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					var dest struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if err := ssh.Unmarshal(req.ExtraData(), &dest); err != nil {
						req.Reject(ssh.Prohibited, "bad payload")
						continue
					}
					target, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(dest.Host, strconv.Itoa(int(dest.Port))))
					if err != nil {
						req.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					ch, r, err := req.Accept()
					if err != nil {
						target.Close()
						continue
					}
					fwdCount.Add(1)
					go ssh.DiscardRequests(r)
					wg.Go(func() {
						defer ch.Close()
						defer target.Close()
						var pipe sync.WaitGroup
						pipe.Go(func() {
							_, _ = io.Copy(ch, target)
							_ = ch.CloseWrite()
						})
						pipe.Go(func() {
							_, _ = io.Copy(target, ch)
							_ = target.Close()
						})
						pipe.Wait()
					})
				}
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = ln.Close(); wg.Wait() })

	tcpAddr := ln.Addr().(*net.TCPAddr)
	return tcpAddr.IP.String(), tcpAddr.Port, signer.PublicKey(), fwdCount
}

type stubTCPDriver struct {
	targetAddr string
	banner     string
	closed     atomic.Bool
}

func (d *stubTCPDriver) Engine() domain.Engine { return domain.EnginePostgres }

func (d *stubTCPDriver) Connect(ctx context.Context, profile domain.Profile, opts engine.ConnectOptions) (engine.Conn, error) {
	if opts.Dial == nil {
		return nil, errors.New("expected SSH tunnel DialFunc")
	}
	c, err := opts.Dial(ctx, "tcp", d.targetAddr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	buf := make([]byte, len(d.banner))
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	if string(buf) != d.banner {
		return nil, errors.New("unexpected banner: " + string(buf))
	}
	return &stubTCPConn{closed: &d.closed}, nil
}

type stubTCPConn struct {
	closed *atomic.Bool
}

func (c *stubTCPConn) Version(context.Context) (string, error) { return "16.4", nil }
func (c *stubTCPConn) ListObjects(context.Context) ([]domain.SchemaObject, error) {
	return []domain.SchemaObject{{Name: "orders", Kind: domain.ObjectTable, Rows: 42}}, nil
}
func (c *stubTCPConn) BrowseTable(context.Context, string, string, int, int) (domain.QueryResult, error) {
	return domain.QueryResult{}, nil
}
func (c *stubTCPConn) ExecQuery(context.Context, string) (domain.QueryResult, error) {
	return domain.QueryResult{}, nil
}
func (c *stubTCPConn) ExecTransaction(context.Context, []domain.ParamStatement) (int64, error) {
	return 0, nil
}
func (c *stubTCPConn) Close() error {
	c.closed.Store(true)
	return nil
}

func TestRuntimeTwoHopSSHTunnelHostKeyChallengeAndTeardown(t *testing.T) {
	const sshUser = "ops"
	const sshPass = "bastion-secret-99"
	const dbBanner = "PG-TUNNEL-OK"

	// Target TCP stub behind hop 2.
	dbLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbLn.Close() })
	go func() {
		for {
			c, err := dbLn.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, dbBanner)
			_ = c.Close()
		}
	}()

	hop1Host, hop1Port, hop1Pub, hop1Fwds := startTestSSHServer(t, sshUser, sshPass)
	hop2Host, hop2Port, hop2Pub, hop2Fwds := startTestSSHServer(t, sshUser, sshPass)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "profiles.json")
	khPath := filepath.Join(dir, "known_hosts")
	secStore := secret.NewMemoryStore()
	drv := &stubTCPDriver{targetAddr: dbLn.Addr().String(), banner: dbBanner}

	rt, err := NewWithHostKeys(
		config.NewStore(cfgPath),
		secStore,
		tunnel.NewHostKeyStoreIsolated(khPath),
		drv,
	)
	if err != nil {
		t.Fatalf("NewWithHostKeys: %v", err)
	}

	profile := domain.Profile{
		Name:        "orders-prod",
		Engine:      domain.EnginePostgres,
		Host:        "127.0.0.1",
		Port:        dbLn.Addr().(*net.TCPAddr).Port,
		Database:    "orders",
		Username:    "readonly",
		Env:         domain.EnvProd,
		ReadOnly:    true,
		SSHEnabled:  true,
		SSHJumpPath: net.JoinHostPort(hop1Host, strconv.Itoa(hop1Port)),
		SSHHost:     hop2Host,
		SSHPort:     hop2Port,
		SSHUser:     sshUser,
		SSHAuth:     domain.SSHAuthPassword,
	}

	var saveErr error
	rt.SaveAndConnect(profile, Credentials{
		Password:    "db-secret-42",
		SSHPassword: sshPass,
	}, func(err error) { saveErr = err })
	rt.WaitIdle()

	// Step 1: Hop 1 is unknown -> runtime surfaces HostKeyChallenge without connecting.
	snap := rt.Snapshot()
	if snap.PendingHostKey == nil || snap.PendingHostKey.Mismatch {
		t.Fatalf("expected unknown host key challenge for hop 1, got %+v (saveErr=%v)", snap.PendingHostKey, saveErr)
	}
	if snap.PendingHostKey.Fingerprint != ssh.FingerprintSHA256(hop1Pub) {
		t.Fatalf("hop 1 fingerprint mismatch: got %q want %q", snap.PendingHostKey.Fingerprint, ssh.FingerprintSHA256(hop1Pub))
	}

	// Approve hop 1 persistently -> runtime automatically retries and stops at unknown hop 2.
	if err := rt.TrustHostKey(true); err != nil {
		t.Fatalf("TrustHostKey(hop1): %v", err)
	}
	rt.WaitIdle()

	snap = rt.Snapshot()
	if snap.PendingHostKey == nil || snap.PendingHostKey.Mismatch {
		t.Fatalf("expected unknown host key challenge for hop 2, got %+v", snap.PendingHostKey)
	}
	if snap.PendingHostKey.Fingerprint != ssh.FingerprintSHA256(hop2Pub) {
		t.Fatalf("hop 2 fingerprint mismatch: got %q want %q", snap.PendingHostKey.Fingerprint, ssh.FingerprintSHA256(hop2Pub))
	}

	// Approve hop 2 for session only -> runtime automatically retries and connects end-to-end!
	if err := rt.TrustHostKey(false); err != nil {
		t.Fatalf("TrustHostKey(hop2): %v", err)
	}
	rt.WaitIdle()

	snap = rt.Snapshot()
	if snap.PendingHostKey != nil {
		t.Fatalf("expected challenge cleared after connect, got %+v", snap.PendingHostKey)
	}
	if snap.Active != 0 || !snap.Connections[0].Connected || snap.Connections[0].Version != "16.4" {
		t.Fatalf("expected connected via 2-hop SSH tunnel, got %+v", snap.Connections)
	}
	if hop1Fwds.Load() < 1 || hop2Fwds.Load() < 1 {
		t.Fatalf("expected traffic forwarded through both SSH hops, hop1=%d hop2=%d", hop1Fwds.Load(), hop2Fwds.Load())
	}

	// Verify neither DB password nor SSH password leaked into profiles.json.
	rawCfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"db-secret-42", sshPass} {
		if strings.Contains(string(rawCfg), leaked) {
			t.Fatalf("secret %q leaked into profiles.json", leaked)
		}
	}

	// Verify Close() tears down both DB connection and multi-hop SSH tunnel cleanly.
	if err := rt.Close(); err != nil {
		t.Fatalf("rt.Close: %v", err)
	}
	if !drv.closed.Load() {
		t.Fatal("expected DB connection to be closed on runtime.Close")
	}
}
