package tunnel_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AnalyseDeCircuit/kyandb/internal/tunnel"
)

// Creation-time labels distinguish the stream under test from its live sibling
// and from the SSH fixture. A successful Close alone is not an exit signal.
func requireWorkersGone(t *testing.T, label string) {
	t.Helper()
	end := time.Now().Add(2 * time.Second)
	for {
		var profile bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&profile, 1); err != nil {
			t.Fatal(err)
		}
		var remaining []string
		for _, stack := range strings.Split(profile.String(), "\n\n") {
			if strings.Contains(stack, `"tunnel-worker":"`+label+`"`) {
				remaining = append(remaining, stack)
			}
		}
		if len(remaining) == 0 {
			return
		}
		if time.Now().After(end) {
			t.Errorf("workers still running for %s:\n%s", label, strings.Join(remaining, "\n"))
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The relay can withhold server packets without closing the TCP connection.
// SSH handshakes, channel windows and close acknowledgments are otherwise real.
type jumpRelay struct {
	hop      tunnel.Hop
	sessions chan net.Conn
	paused   atomic.Bool
	resume   chan struct{}
}

func relayJump(t *testing.T, hop tunnel.Hop) *jumpRelay {
	r := &jumpRelay{hop: hop, sessions: make(chan net.Conn, 4), resume: make(chan struct{})}
	upstream := net.JoinHostPort(hop.Host, strconv.Itoa(hop.Port))
	addr := serve(t, "tcp", "127.0.0.1:0", func(ctx context.Context, client net.Conn) {
		server, err := (&net.Dialer{}).DialContext(ctx, "tcp", upstream)
		if err != nil {
			return
		}
		r.sessions <- client
		done := make(chan struct{})
		go func() { defer close(done); io.Copy(server, client); server.Close() }()
		defer func() { client.Close(); server.Close(); <-done }()
		buf := make([]byte, 32768)
		for {
			n, err := server.Read(buf)
			if n > 0 {
				if r.paused.Load() {
					select {
					case <-r.resume:
					case <-ctx.Done():
						return
					}
				}
				if _, writeErr := client.Write(buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	})
	tcp, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	r.hop.Host, r.hop.Port = tcp.IP.String(), tcp.Port
	return r
}

func openRoute(t *testing.T, m *tunnel.Manager, hops ...tunnel.Hop) *tunnel.Tunnel {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h, err := m.Open(ctx, hops)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func dialStream(t *testing.T, h *tunnel.Tunnel, target string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c, err := h.DialContext(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestCloseWithoutParentAck(t *testing.T) {
	_, host := key(t)
	parent := fixture(t, host, nil, nil)
	relay := relayJump(t, parent.hop)
	defer func() {
		if relay.paused.Swap(false) {
			close(relay.resume)
		}
	}()
	target := echo(t)
	left := fixture(t, host, nil, &pausedForward{target: target})
	right := fixture(t, host, nil, nil)
	m := checkedManager(t, host.PublicKey())
	b := openRoute(t, m, relay.hop, right.hop)
	var a *tunnel.Tunnel
	pprof.Do(t.Context(), pprof.Labels("tunnel-worker", "closing-child"), func(ctx context.Context) {
		var err error
		a, err = m.Open(ctx, tunnel.Chain{relay.hop, left.hop})
		if err != nil {
			t.Fatal(err)
		}
	})
	sibling := dialStream(t, b, target)
	checkEcho(t, sibling)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dial := make(chan error, 1)
	go func() { _, err := a.DialContext(ctx, "tcp", target); dial <- err }()
	receive(t, left.forwards)
	cancel()
	if err := receive(t, dial); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	relay.paused.Store(true)
	closed := make(chan error, 1)
	go func() { closed <- a.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing leaf waited for the parent's channel-close acknowledgment")
	}
	requireWorkersGone(t, "closing-child")
	relay.paused.Store(false)
	close(relay.resume)
	checkEcho(t, sibling)
	if parent.count.Load() != 1 {
		t.Fatal("shared parent was replaced")
	}
}

func TestStreamDeadlines(t *testing.T) {
	_, host := key(t)
	s := fixture(t, host, nil, nil)
	m := checkedManager(t, host.PublicKey())
	h := openRoute(t, m, s.hop)
	target := echo(t)
	sibling := dialStream(t, h, target)
	t.Run("read", func(t *testing.T) {
		c := dialStream(t, h, target)
		result := make(chan error, 1)
		started := make(chan struct{})
		go func() {
			var b [1]byte
			close(started)
			_, err := c.Read(b[:])
			result <- err
		}()
		<-started
		if err := c.SetReadDeadline(time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := receive(t, result); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("blocked read: %v", err)
		}
		checkEcho(t, sibling)
		if err := c.SetDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
		checkEcho(t, c)
	})
	t.Run("write", func(t *testing.T) {
		received, resume := make(chan struct{}), make(chan struct{})
		const size = 16 << 20
		sink := serve(t, "tcp", "127.0.0.1:0", func(ctx context.Context, c net.Conn) {
			if _, err := io.CopyN(io.Discard, c, 1); err != nil {
				return
			}
			close(received)
			select {
			case <-resume:
			case <-ctx.Done():
				return
			}
			if n, err := io.Copy(io.Discard, c); err == nil && n == size-1 {
				c.Write([]byte("ok"))
			}
		})
		c := dialStream(t, h, sink)
		data := make([]byte, size)
		type writeResult struct {
			n   int
			err error
		}
		written := make(chan writeResult, 1)
		go func() { n, err := c.Write(data); written <- writeResult{n, err} }()
		receive(t, received)
		if err := c.SetWriteDeadline(time.Now()); err != nil {
			t.Fatal(err)
		}
		w := receive(t, written)
		if !errors.Is(w.err, os.ErrDeadlineExceeded) || w.n >= size {
			t.Fatalf("blocked write: n=%d err=%v", w.n, w.err)
		}
		checkEcho(t, sibling)
		close(resume)
		if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write(data[w.n:]); err != nil {
			t.Fatal(err)
		}
		if err := c.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		var ack [2]byte
		if _, err := io.ReadFull(c, ack[:]); err != nil || string(ack[:]) != "ok" {
			t.Fatalf("resumed stream: %q %v", ack, err)
		}
	})
}

func TestReplaceDisconnectedParent(t *testing.T) {
	_, host := key(t)
	parent, leaf := fixture(t, host, nil, nil), fixture(t, host, nil, nil)
	relay := relayJump(t, parent.hop)
	m := checkedManager(t, host.PublicKey())
	old := openRoute(t, m, relay.hop, leaf.hop)
	target := echo(t)
	var oldStream net.Conn
	pprof.Do(t.Context(), pprof.Labels("tunnel-worker", "old-stream"), func(ctx context.Context) {
		var err error
		oldStream, err = old.DialContext(ctx, "tcp", target)
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Cleanup(func() { oldStream.Close() })
	checkEcho(t, oldStream)
	const tail = "final packet"
	if _, err := io.WriteString(oldStream, tail); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := io.ReadFull(oldStream, b[:]); err != nil || b[0] != tail[0] {
		t.Fatalf("first byte: %q, %v", b, err)
	}
	receive(t, relay.sessions).Close()
	if rest, err := io.ReadAll(oldStream); err != nil || string(rest) != tail[1:] {
		t.Fatalf("buffered tail: %q, %v", rest, err)
	}
	fresh := openRoute(t, m, relay.hop, leaf.hop)
	c := dialStream(t, fresh, target)
	requireWorkersGone(t, "old-stream")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	// Releasing the old reference must neither close nor evict the replacement.
	another := openRoute(t, m, relay.hop, leaf.hop)
	if err := another.Close(); err != nil {
		t.Fatal(err)
	}
	checkEcho(t, c)
	if parent.count.Load() != 2 {
		t.Fatalf("parent sessions: %d", parent.count.Load())
	}
}

func TestDeadlineAfterReadEOF(t *testing.T) {
	_, host := key(t)
	server := fixture(t, host, nil, nil)
	m := checkedManager(t, host.PublicKey())
	h := openRoute(t, m, server.hop)
	sibling := dialStream(t, h, echo(t))
	received := make(chan struct{})
	target := serve(t, "tcp", "127.0.0.1:0", func(ctx context.Context, c net.Conn) {
		if err := c.(*net.TCPConn).CloseWrite(); err != nil {
			return
		}
		if _, err := io.CopyN(io.Discard, c, 1); err != nil {
			return
		}
		close(received)
		<-ctx.Done()
	})
	c := dialStream(t, h, target)
	var b [1]byte
	if _, err := c.Read(b[:]); err != io.EOF {
		t.Fatalf("expected peer EOF: %v", err)
	}
	written := make(chan error, 1)
	go func() { _, err := c.Write(make([]byte, 16<<20)); written <- err }()
	receive(t, received)
	if err := c.SetDeadline(time.Now()); err != nil {
		t.Errorf("SetDeadline after EOF: %v", err)
	}
	if err := receive(t, written); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("remaining write direction: %v", err)
	}
	checkEcho(t, sibling)
}
