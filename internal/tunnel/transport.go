package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/ssh"
)

// SSH has no cancel-channel-open message. Only unanswered opens count toward
// this limit; any number of established streams can share the transport.
const maxPendingOpens = 32

var ErrPendingOpens = errors.New("SSH peer has not answered canceled channel opens")
var errTransportLost = fmt.Errorf("SSH transport disconnected: %w", net.ErrClosed)

type dialResult struct {
	conn net.Conn
	err  error
}

type transport struct {
	client       *ssh.Client
	disconnected atomic.Bool
	life         context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	pending      map[chan dialResult]<-chan struct{}
	changed      chan struct{}
	workers      sync.WaitGroup
}

func newTransport(client *ssh.Client) *transport {
	ctx, cancel := context.WithCancel(context.Background())
	t := &transport{client: client, life: ctx, cancel: cancel, pending: make(map[chan dialResult]<-chan struct{})}
	t.workers.Go(func() {
		client.Wait() // Any terminal result retires this generation from the pool.
		t.mu.Lock()
		t.disconnected.Store(true)
		t.wakeWaitersLocked()
		t.mu.Unlock()
	})
	return t
}

func (t *transport) wakeWaitersLocked() {
	if t.changed != nil {
		close(t.changed)
		t.changed = nil
	}
}

func (t *transport) reserve(ctx context.Context, reply chan dialResult) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if t.life.Err() != nil {
			return net.ErrClosed
		}
		if t.disconnected.Load() {
			return errTransportLost
		}
		if len(t.pending) < maxPendingOpens {
			t.pending[reply] = ctx.Done()
			t.workers.Add(1)
			return nil
		}
		// Queue healthy bursts. Once abandoned opens consume capacity, a
		// live caller without a deadline must not keep new callers waiting.
		for _, done := range t.pending {
			select {
			case <-done:
				return ErrPendingOpens
			default:
			}
		}
		if t.changed == nil {
			t.changed = make(chan struct{})
		}
		changed := t.changed
		t.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		t.mu.Lock()
	}
}

func (t *transport) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, net.UnknownNetworkError("SSH forwarding requires TCP")
	}
	reply := make(chan dialResult)
	if err := t.reserve(ctx, reply); err != nil {
		return nil, err
	}
	done := ctx.Done()
	go func() {
		defer t.workers.Done()
		conn, err := t.client.Dial(network, addr)
		if err != nil {
			var rejected *ssh.OpenChannelError
			var address *net.AddrError
			var port *strconv.NumError
			if errors.As(err, &rejected) || errors.As(err, &address) || errors.As(err, &port) {
				// Destination and channel rejection text need not be safe to log.
				err = errors.New("SSH forwarding request failed")
			} else {
				// After TCP address parsing, a failed SSH open is either a
				// peer rejection or failure of the multiplexed transport.
				t.disconnected.Store(true)
				err = errTransportLost
			}
		}
		select {
		case reply <- dialResult{conn, err}:
			conn = nil // Ownership passed to the caller.
		case <-done:
		case <-t.life.Done():
		}
		if conn != nil {
			conn.Close()
		}
		t.mu.Lock()
		delete(t.pending, reply)
		t.wakeWaitersLocked()
		t.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		t.mu.Lock()
		t.wakeWaitersLocked()
		t.mu.Unlock()
		return nil, ctx.Err()
	case <-t.life.Done():
		return nil, net.ErrClosed
	case res := <-reply:
		return res.conn, res.err
	}
}

func (t *transport) close() error {
	t.mu.Lock()
	t.cancel()
	t.wakeWaitersLocked()
	t.mu.Unlock()
	err := t.client.Close()
	t.workers.Wait()
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
