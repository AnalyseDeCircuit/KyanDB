package tunnel_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
	"github.com/AnalyseDeCircuit/kyandb/internal/tunnel"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestParseProxyJump(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"jump", []string{"@jump:22"}},
		{"OPS@A:2201, host:2202, dev@[::1]:22", []string{"OPS@a:2201", "@host:2202", "dev@[::1]:22"}},
		{"me@[2001:db8::2]", []string{"me@[2001:db8::2]:22"}},
	} {
		t.Run(tc.in, func(t *testing.T) {
			c, err := tunnel.ParseProxyJump(tc.in)
			if err != nil || len(c) != len(tc.want) {
				t.Fatalf("parse: %v, hops=%d", err, len(c))
			}
			for i, h := range c {
				if got := h.User + "@" + net.JoinHostPort(h.Host, strconv.Itoa(h.Port)); got != tc.want[i] {
					t.Errorf("hop %d: %q, want %q", i, got, tc.want[i])
				}
			}
		})
	}
	for _, in := range []string{"", ",a", "a,", "a,,b", ":22", "a:", "a:0", "a:65536", "a:db", "a:-2", "a b", "user:p@host", "@host", "a@b@c", "::1", "[host]:22", "[::1", "[::1]x", "[::1]:", "ssh://a"} {
		if _, err := tunnel.ParseProxyJump(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestProfileRoute(t *testing.T) {
	p := domain.Profile{SSHEnabled: true, SSHJumpPath: "other@A,b", SSHHost: "[::1]", SSHPort: 2222, SSHUser: "ops", Host: "database"}
	c, err := tunnel.ChainFromProfile(p, tunnel.AgentAuth())
	if err != nil || len(c) != 3 || c[0].User != "other" || c[1].User != "ops" || c[2].Host != "::1" || c[2].Port != 2222 || c[2].User != "ops" {
		t.Fatalf("unexpected profile route: length=%d, err=%v", len(c), err)
	}
	p.SSHHost = ""
	c, err = tunnel.ChainFromProfile(p, tunnel.AgentAuth())
	if err != nil || len(c) != 2 || c[1].Host != "b" {
		t.Fatal("jump-only profile lost final hop")
	}
}

const password = "fixture-only-password-37"
const passphrase = "fixture-only-key-phrase-19"

func key(t *testing.T) (ed25519.PrivateKey, ssh.Signer) {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return k, s
}

func checkedManager(t *testing.T, key ssh.PublicKey) *tunnel.Manager {
	t.Helper()
	m, err := tunnel.NewManager(ssh.FixedHostKey(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m
}

// Listener shutdown interrupts both Accept and every outstanding connection.
func serve(t *testing.T, network, addr string, fn func(context.Context, net.Conn)) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ln, err := (&net.ListenConfig{}).Listen(ctx, network, addr)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				stop := context.AfterFunc(ctx, func() { c.Close() })
				defer stop()
				defer c.Close()
				fn(ctx, c)
			})
		}
	})
	t.Cleanup(func() { cancel(); ln.Close(); wg.Wait() })
	return ln.Addr().String()
}

type localSSH struct {
	hop       tunnel.Hop
	count     atomic.Int32
	connected chan chan struct{}
	forwards  chan string
}

type pausedForward struct {
	target string
	resume <-chan struct{}
}

func fixture(t *testing.T, host ssh.Signer, allowed ssh.PublicKey, pause *pausedForward) *localSSH {
	t.Helper()
	s := &localSSH{connected: make(chan chan struct{}, 16), forwards: make(chan string, 16)}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, b []byte) (*ssh.Permissions, error) {
			if meta.User() == "ops" && string(b) == password {
				return nil, nil
			}
			return nil, errors.New("bad password")
		},
		PublicKeyCallback: func(meta ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() == "ops" && allowed != nil && bytes.Equal(k.Marshal(), allowed.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("bad key")
		},
	}
	cfg.AddHostKey(host)
	addr := serve(t, "tcp", "127.0.0.1:0", func(ctx context.Context, c net.Conn) {
		sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
		if err != nil {
			return
		}
		defer sc.Close()
		s.count.Add(1)
		closed := make(chan struct{})
		s.connected <- closed
		defer close(closed)
		ctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Go(func() { ssh.DiscardRequests(reqs) })
		for req := range chans {
			wg.Go(func() {
				var dest struct {
					Host       string
					Port       uint32
					Origin     string
					OriginPort uint32
				}
				if req.ChannelType() != "direct-tcpip" || ssh.Unmarshal(req.ExtraData(), &dest) != nil || dest.Host != "127.0.0.1" {
					req.Reject(ssh.Prohibited, "local TCP only")
					return
				}
				addr := net.JoinHostPort(dest.Host, strconv.Itoa(int(dest.Port)))
				s.forwards <- addr
				if pause != nil && addr == pause.target {
					select {
					case <-pause.resume:
					case <-ctx.Done():
						return
					}
				}
				target, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
				if err != nil {
					req.Reject(ssh.ConnectionFailed, "unavailable")
					return
				}
				defer target.Close()
				ch, r, err := req.Accept()
				if err != nil {
					return
				}
				defer ch.Close()
				stop := context.AfterFunc(ctx, func() { target.Close() })
				defer stop()
				wg.Go(func() { ssh.DiscardRequests(r) })
				// A forwarded stream can reach EOF in either direction first.
				writeDone := make(chan struct{})
				wg.Go(func() { defer close(writeDone); io.Copy(target, ch); target.(*net.TCPConn).CloseWrite() })
				io.Copy(ch, target)
				ch.CloseWrite()
				<-writeDone
			})
		}
		cancel()
		wg.Wait()
	})
	hostName, p, _ := net.SplitHostPort(addr)
	port, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	s.hop = tunnel.Hop{User: "ops", Host: hostName, Port: port, Auth: tunnel.PasswordAuth(secret.NewString(password))}
	return s
}

func receive[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for peer")
	}
	var zero T
	return zero
}

func echo(t *testing.T) string {
	return serve(t, "tcp", "127.0.0.1:0", func(_ context.Context, c net.Conn) { io.Copy(c, c) })
}

func exchange(t *testing.T, tun *tunnel.Tunnel, addr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c, err := tun.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	checkEcho(t, c)
}

func checkEcho(t *testing.T, c net.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	payload := []byte("KyanDB\x00forward\xff\r\n")
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("echo: %x, %v", got, err)
	}
}

func TestSharedParent(t *testing.T) {
	_, host := key(t)
	parent := fixture(t, host, nil, nil)
	left, right := fixture(t, host, nil, nil), fixture(t, host, nil, nil)
	target := echo(t)
	m := checkedManager(t, host.PublicKey())
	type opened struct {
		handle *tunnel.Tunnel
		err    error
	}
	open := func(leaf tunnel.Hop) <-chan opened {
		result := make(chan opened, 1)
		go func() { h, err := m.Open(t.Context(), tunnel.Chain{parent.hop, leaf}); result <- opened{h, err} }()
		return result
	}
	a, b := open(left.hop), open(right.hop)
	one, two := receive(t, a), receive(t, b)
	if one.err != nil || two.err != nil {
		t.Fatalf("open: %v, %v", one.err, two.err)
	}
	closed := receive(t, parent.connected)
	if parent.count.Load() != 1 {
		t.Fatalf("opened %d parent transports", parent.count.Load())
	}
	observed := map[string]bool{receive(t, parent.forwards): true}
	observed[receive(t, parent.forwards)] = true
	for _, f := range []*localSSH{left, right} {
		if !observed[net.JoinHostPort(f.hop.Host, strconv.Itoa(f.hop.Port))] {
			t.Fatal("leaf was not reached through parent")
		}
	}
	exchange(t, one.handle, target)
	if receive(t, left.forwards) != target {
		t.Fatal("wrong target")
	}
	leftClosed := receive(t, left.connected)
	if err := one.handle.Close(); err != nil {
		t.Fatal(err)
	}
	receive(t, leftClosed)
	// Even an established parent must not satisfy a different credential.
	bad := parent.hop
	bad.Auth = tunnel.PasswordAuth(secret.NewString("different-password"))
	if _, err := m.Open(t.Context(), tunnel.Chain{bad, right.hop}); err == nil {
		t.Fatal("parent cache bypassed authentication")
	}
	exchange(t, two.handle, target)
	if receive(t, right.forwards) != target {
		t.Fatal("second leaf lost forwarding")
	}
	if err := two.handle.Close(); err != nil {
		t.Fatal(err)
	}
	receive(t, closed)
	if err := two.handle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelHandshake(t *testing.T) {
	for _, byManager := range []bool{false, true} {
		t.Run(fmt.Sprintf("manager=%t", byManager), func(t *testing.T) {
			started, closed := make(chan struct{}), make(chan struct{})
			addr := serve(t, "tcp", "127.0.0.1:0", func(_ context.Context, c net.Conn) {
				buf := make([]byte, 1)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				close(started)
				io.Copy(io.Discard, c)
				close(closed)
			})
			c, err := tunnel.ParseProxyJump("ops@" + addr)
			if err != nil {
				t.Fatal(err)
			}
			c[0].Auth = tunnel.PasswordAuth(secret.NewString(password))
			_, host := key(t)
			var parent *localSSH
			if byManager {
				// A forwarded stream closes via the parent SSH transport, not a TCP deadline.
				parent = fixture(t, host, nil, nil)
				c = append(tunnel.Chain{parent.hop}, c...)
			}
			m := checkedManager(t, host.PublicKey())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := m.Open(ctx, c); result <- err }()
			receive(t, started)
			if byManager {
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			if err := receive(t, result); !errors.Is(err, context.Canceled) {
				t.Fatalf("open: %v", err)
			}
			receive(t, closed)
			if parent != nil {
				receive(t, receive(t, parent.connected))
			}
		})
	}
}

func TestCancelForward(t *testing.T) {
	_, host := key(t)
	resume := make(chan struct{})
	lateClosed := make(chan struct{})
	stalled := serve(t, "tcp", "127.0.0.1:0", func(_ context.Context, c net.Conn) {
		var b [1]byte
		if _, err := c.Read(b[:]); err == io.EOF {
			close(lateClosed)
		}
	})
	s := fixture(t, host, nil, &pausedForward{stalled, resume})
	target := echo(t)
	m := checkedManager(t, host.PublicKey())
	h, err := m.Open(t.Context(), tunnel.Chain{s.hop})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := h.DialContext(t.Context(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	if receive(t, s.forwards) != target {
		t.Fatal("wrong initial target")
	}
	checkEcho(t, existing)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := h.DialContext(ctx, "tcp", stalled); result <- err }()
	if receive(t, s.forwards) != stalled {
		t.Fatal("stalled request did not reach peer")
	}
	cancel()
	if err := receive(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("dial: %v", err)
	}
	// The unanswered request stays on the wire throughout both round trips.
	exchange(t, h, target)
	checkEcho(t, existing)
	if s.count.Load() != 1 {
		t.Fatal("SSH transport was replaced")
	}
	close(resume)
	receive(t, lateClosed)
	closed := receive(t, s.connected)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	receive(t, closed)
	if _, err := h.DialContext(t.Context(), "tcp", target); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed handle: %v", err)
	}
}

func TestUnansweredForwardLimit(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("oneLive=%t", live), func(t *testing.T) { testUnansweredForwardLimit(t, live) })
	}
}

func testUnansweredForwardLimit(t *testing.T, keepOneLive bool) {
	_, host := key(t)
	target := echo(t)
	s := fixture(t, host, nil, &pausedForward{target: target})
	m := checkedManager(t, host.PublicKey())
	h, err := m.Open(t.Context(), tunnel.Chain{s.hop})
	if err != nil {
		t.Fatal(err)
	}

	healthy := echo(t)
	existing := dialStream(t, h, healthy)
	if receive(t, s.forwards) != healthy {
		t.Fatal("wrong existing stream target")
	}
	// No stalled channel gets a reply, even during shutdown. Count requests observed
	// on the wire rather than inspecting the transport's worker count.
	for i := 0; i < 32; i++ {
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() { _, err := h.DialContext(ctx, "tcp", target); result <- err }()
		addr := receive(t, s.forwards)
		if keepOneLive && i == 31 {
			defer cancel()
			if addr != target {
				t.Fatal("wrong live forward target")
			}
			break
		}
		cancel()
		if addr != target || !errors.Is(receive(t, result), context.Canceled) {
			t.Fatal("pending forward was not canceled")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := h.DialContext(ctx, "tcp", target); !errors.Is(err, tunnel.ErrPendingOpens) {
		t.Fatalf("saturated transport: %v", err)
	}
	checkEcho(t, existing)
	closed := receive(t, s.connected)
	shutdown := make(chan error, 1)
	go func() { shutdown <- m.Close() }()
	if err := receive(t, shutdown); err != nil {
		t.Fatal(err)
	}
	receive(t, closed)
}

func TestHostKeyVerification(t *testing.T) {
	if _, err := tunnel.NewManager(nil); !errors.Is(err, tunnel.ErrHostKeyRequired) {
		t.Fatalf("nil verifier: %v", err)
	}
	_, host := key(t)
	_, other := key(t)
	s := fixture(t, host, nil, nil)
	m := checkedManager(t, other.PublicKey())
	if _, err := m.Open(t.Context(), tunnel.Chain{s.hop}); !errors.Is(err, tunnel.ErrHostKeyRejected) {
		t.Fatalf("wrong server key: %v", err)
	}
	if s.count.Load() != 0 {
		t.Fatal("authenticated before verifying host key")
	}
}

func TestPrivateKeyAuth(t *testing.T) {
	private, signer := key(t)
	_, host := key(t)
	s := fixture(t, host, signer.PublicKey(), nil)
	m := checkedManager(t, host.PublicKey())
	target := echo(t)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(block)
	path := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	for _, fromFile := range []bool{false, true} {
		pass := secret.NewBytes([]byte(passphrase))
		buf := pass.Raw()
		hop := s.hop
		if fromFile {
			hop.Auth, err = tunnel.KeyFileAuth(path, pass)
		} else {
			k := secret.NewBytes(encoded)
			raw := k.Raw()
			hop.Auth, err = tunnel.KeyAuth(k, pass)
			if k.Raw() != nil || !allZero(raw) {
				t.Fatal("PEM bytes retained")
			}
		}
		if err != nil || pass.Raw() != nil || !allZero(buf) {
			t.Fatalf("key consumption: %v", err)
		}
		for _, v := range []any{s.hop.Auth, hop.Auth, s.hop, hop, tunnel.Chain{hop}} {
			for _, format := range []string{"%+v", "%#v"} {
				got := fmt.Sprintf(format, v)
				if got != "[REDACTED]" || strings.Contains(got, password) || strings.Contains(got, passphrase) {
					t.Fatal("credential formatting is not redacted")
				}
			}
		}
		h, err := m.Open(t.Context(), tunnel.Chain{hop})
		if err != nil {
			t.Fatal(err)
		}
		exchange(t, h, target)
		if err := h.Close(); err != nil {
			t.Fatal(err)
		}
	}
	bad := secret.NewBytes([]byte("wrong"))
	raw := bad.Raw()
	if _, err := tunnel.KeyAuth(secret.NewBytes(encoded), bad); err == nil || !allZero(raw) {
		t.Fatal("wrong passphrase was accepted or not erased")
	}
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestAgentAuth(t *testing.T) {
	private, signer := key(t)
	_, host := key(t)
	s := fixture(t, host, signer.PublicKey(), nil)
	m := checkedManager(t, host.PublicKey())
	hop := s.hop
	hop.Auth = tunnel.AgentAuth()
	t.Setenv("SSH_AUTH_SOCK", "")
	if _, err := m.Open(t.Context(), tunnel.Chain{hop}); err == nil || !strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
		t.Fatalf("missing agent: %v", err)
	}
	dir, err := os.MkdirTemp("", "kyan-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	socket := filepath.Join(dir, "sock")
	t.Setenv("SSH_AUTH_SOCK", socket)
	if _, err := m.Open(t.Context(), tunnel.Chain{hop}); err == nil {
		t.Fatal("unreachable agent accepted")
	}
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	serve(t, "unix", socket, func(_ context.Context, c net.Conn) { agent.ServeAgent(ring, c) })
	h, err := m.Open(t.Context(), tunnel.Chain{hop})
	if err != nil {
		t.Fatal(err)
	}
	exchange(t, h, echo(t))
}
