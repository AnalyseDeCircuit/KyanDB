package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
	"golang.org/x/crypto/ssh"
)

var ErrHostKeyRequired = errors.New("SSH host key verification callback is required")
var ErrHostKeyRejected = errors.New("SSH host key verification failed")

type hopKey struct {
	parent                  *transport
	user, address, identity string
}

type pooled struct {
	key   hopKey
	ready chan struct{}
	conn  *transport
	refs  int
	err   error
}

// A manager has a single trust policy. Connections authenticated under a
// different verifier can never satisfy a cache hit.
type Manager struct {
	verify   ssh.HostKeyCallback
	life     context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	parents  map[hopKey]*pooled
	handles  map[*Tunnel]struct{}
	opens    sync.WaitGroup
	done     chan struct{}
	closeErr error
}

func NewManager(verify ssh.HostKeyCallback) (*Manager, error) {
	if verify == nil {
		return nil, ErrHostKeyRequired
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{verify: verify, life: ctx, cancel: cancel, parents: make(map[hopKey]*pooled), handles: make(map[*Tunnel]struct{}), done: make(chan struct{})}, nil
}

type Tunnel struct {
	manager  *Manager
	parents  []*pooled
	leaf     *transport
	life     context.Context
	cancel   context.CancelFunc
	dials    sync.WaitGroup
	done     chan struct{}
	closeErr error
}

func withLifetime(ctx, life context.Context) (context.Context, func()) {
	op, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(life, cancel)
	if life.Err() != nil {
		cancel()
	}
	return op, func() { stop(); cancel() }
}

// Open borrows each parent in order. The leaf is private so closing a handle
// also closes its database streams without touching another handle's streams.
func (m *Manager) Open(ctx context.Context, chain Chain) (*Tunnel, error) {
	if len(chain) == 0 {
		return nil, errors.New("empty SSH route")
	}
	m.mu.Lock()
	if m.life.Err() != nil {
		m.mu.Unlock()
		return nil, net.ErrClosed
	}
	m.opens.Add(1)
	m.mu.Unlock()
	defer m.opens.Done()
	ctx, finish := withLifetime(ctx, m.life)
	defer finish()
	return m.openRoute(ctx, chain, true)
}

func (m *Manager) openRoute(ctx context.Context, chain Chain, retryLostParent bool) (*Tunnel, error) {
	var parents []*pooled
	var current *transport
	for i, h := range chain {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, m.release(parents))
		}
		h, addr, err := h.endpoint()
		if err != nil {
			return nil, errors.Join(err, m.release(parents))
		}
		auth, release, err := h.Auth.resolve(ctx)
		if err != nil {
			return nil, errors.Join(operationError(ctx, err), m.release(parents))
		}
		key := hopKey{parent: current, user: h.User, address: addr, identity: auth.identity}
		if i+1 < len(chain) {
			var p *pooled
			p, err = m.acquire(ctx, key, auth.method)
			if err == nil {
				parents = append(parents, p)
				current = p.conn
			}
		} else {
			current, err = m.connect(ctx, key, auth.method)
		}
		release()
		if err != nil {
			cleanupErr := m.release(parents)
			// A pooled parent can disconnect between acquire and channel open.
			// No database stream has been published; never retry a peer rejection.
			if retryLostParent && errors.Is(err, errTransportLost) && cleanupErr == nil && ctx.Err() == nil {
				return m.openRoute(ctx, chain, false)
			}
			return nil, errors.Join(fmt.Errorf("SSH hop %d: %w", i+1, err), cleanupErr)
		}
	}
	life, cancel := context.WithCancel(context.Background())
	t := &Tunnel{manager: m, parents: parents, leaf: current, life: life, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	if m.life.Err() == nil && ctx.Err() == nil {
		m.handles[t] = struct{}{}
		m.mu.Unlock()
		return t, nil
	}
	m.mu.Unlock()
	return nil, errors.Join(operationError(ctx, net.ErrClosed), t.Close())
}

func operationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (m *Manager) acquire(ctx context.Context, key hopKey, auth ssh.AuthMethod) (*pooled, error) {
	for {
		m.mu.Lock()
		p := m.parents[key]
		if p == nil || (p.conn != nil && p.conn.disconnected.Load()) {
			p = &pooled{key: key, ready: make(chan struct{})}
			m.parents[key] = p
			m.mu.Unlock()
			conn, err := m.connect(ctx, key, auth)
			m.mu.Lock()
			p.conn, p.err = conn, err
			if err != nil {
				delete(m.parents, key)
			} else {
				p.refs = 1
			}
			close(p.ready)
			m.mu.Unlock()
			return p, err
		}
		if p.conn != nil {
			p.refs++
			m.mu.Unlock()
			return p, nil
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.ready:
			// The initiating caller's cancellation does not cancel other opens.
			if p.err != nil && !errors.Is(p.err, context.Canceled) && !errors.Is(p.err, context.DeadlineExceeded) {
				return nil, p.err
			}
		}
	}
}

func (m *Manager) connect(ctx context.Context, key hopKey, auth ssh.AuthMethod) (*transport, error) {
	var raw net.Conn
	var err error
	if key.parent == nil {
		raw, err = (&net.Dialer{}).DialContext(ctx, "tcp", key.address)
		if err != nil {
			return nil, operationError(ctx, errors.New("SSH dial: "+secret.ScrubDSN(err.Error())))
		}
	} else {
		raw, err = key.parent.dial(ctx, "tcp", key.address)
		if err != nil {
			return nil, operationError(ctx, err)
		}
	}
	stop := interruptOnCancel(ctx, raw)
	config := &ssh.ClientConfig{User: key.user, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: func(host string, remote net.Addr, k ssh.PublicKey) error {
		if err := m.verify(host, remote, k); err != nil {
			return ErrHostKeyRejected
		}
		return nil
	}}
	conn, chans, reqs, err := ssh.NewClientConn(raw, key.address, config)
	stop()
	if err != nil {
		// Disconnect descriptions are peer-controlled and may echo authentication data.
		if !errors.Is(err, ErrHostKeyRejected) {
			err = errors.New("SSH handshake or authentication failed")
		} else {
			err = ErrHostKeyRejected
		}
		return nil, operationError(ctx, err)
	}
	client := ssh.NewClient(conn, chans, reqs)
	if err := ctx.Err(); err != nil {
		client.Close() // Cancellation may have closed raw before stop returned.
		return nil, err
	}
	return newTransport(client), nil
}

func (m *Manager) release(parents []*pooled) error {
	var errs []error
	for i := len(parents) - 1; i >= 0; i-- {
		p := parents[i]
		m.mu.Lock()
		p.refs--
		last := p.refs == 0
		// An old handle owns this generation, not whatever now serves its key.
		if last && m.parents[p.key] == p {
			delete(m.parents, p.key)
		}
		m.mu.Unlock()
		if last {
			errs = append(errs, p.conn.close())
		}
	}
	return errors.Join(errs...)
}

func (t *Tunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	m := t.manager
	m.mu.Lock()
	if t.life.Err() != nil || m.life.Err() != nil {
		m.mu.Unlock()
		return nil, net.ErrClosed
	}
	t.dials.Add(1)
	m.mu.Unlock()
	defer t.dials.Done()
	ctx, finish := withLifetime(ctx, t.life)
	defer finish()
	return t.leaf.dial(ctx, network, addr)
}

func (t *Tunnel) Close() error {
	m := t.manager
	m.mu.Lock()
	if t.life.Err() != nil {
		m.mu.Unlock()
		<-t.done
		return t.closeErr
	}
	t.cancel()
	m.mu.Unlock()
	leafErr := t.leaf.close()
	t.dials.Wait()
	t.closeErr = errors.Join(leafErr, m.release(t.parents))
	m.mu.Lock()
	delete(m.handles, t)
	close(t.done)
	m.mu.Unlock()
	return t.closeErr
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.life.Err() != nil {
		m.mu.Unlock()
		<-m.done
		return m.closeErr
	}
	m.cancel()
	handles := make([]*Tunnel, 0, len(m.handles))
	for t := range m.handles {
		handles = append(handles, t)
	}
	m.mu.Unlock()
	var errs []error
	for _, t := range handles {
		errs = append(errs, t.Close())
	}
	m.opens.Wait()
	m.closeErr = errors.Join(errs...)
	close(m.done)
	return m.closeErr
}
