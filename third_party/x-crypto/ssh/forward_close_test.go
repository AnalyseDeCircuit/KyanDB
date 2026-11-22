package ssh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Closing the parent, unlike closing a channel, interrupts the physical write.
type stalledWire struct {
	packetConn
	started, resume, disconnected chan struct{}
	once                          sync.Once
}

func (w *stalledWire) writePacket(p []byte) error {
	if p[0] == msgChannelData {
		w.once.Do(func() { close(w.started) })
		select {
		case <-w.resume:
		case <-w.disconnected:
			return net.ErrClosed
		}
	}
	return w.packetConn.writePacket(p)
}

func (w *stalledWire) Close() error {
	err := w.packetConn.Close()
	if err == nil {
		close(w.disconnected)
	}
	return err
}

func assertNoOwner(t *testing.T, owner string) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for {
		var profile bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&profile, 1); err != nil {
			t.Fatal(err)
		}
		present := false
		for _, stack := range strings.Split(profile.String(), "\n\n") {
			if strings.Contains(stack, `"forward-owner":"`+owner+`"`) {
				if time.Now().After(until) {
					t.Fatalf("%s still running:\n%s", owner, stack)
				}
				present = true
			}
		}
		if !present {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func TestForwardPermanentWriteStall(t *testing.T) {
	for _, action := range []string{"resume", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			local, peer := memPipe()
			wire := &stalledWire{packetConn: local, started: make(chan struct{}), resume: make(chan struct{}), disconnected: make(chan struct{})}
			var m *mux
			pprof.Do(t.Context(), pprof.Labels("forward-owner", "parent"), func(context.Context) { m = newMux(wire) })
			defer m.Close()
			client := &Client{Conn: &connection{mux: m}}
			var ch *chanConn
			var id uint32
			pprof.Do(t.Context(), pprof.Labels("forward-owner", "child"), func(context.Context) { ch, id = openTestForward(t, client, peer) })
			sibling, other := openTestForward(t, client, peer)
			data := []byte("accepted data")
			written := make(chan error, 1)
			go pprof.Do(t.Context(), pprof.Labels("forward-owner", "child"), func(context.Context) { _, err := ch.Write(data); written <- err })
			<-wire.started
			closed := make(chan error, 1)
			start := time.Now()
			go pprof.Do(t.Context(), pprof.Labels("forward-owner", "child"), func(context.Context) { closed <- ch.Close() })
			<-ch.Channel.(*channel).forwardDone
			if action == "disconnect" {
				if err := peer.writePacket(Marshal(channelCloseMsg{PeersID: id})); err != nil {
					t.Fatal(err)
				}
			}
			if err := peer.writePacket(Marshal(channelDataMsg{PeersID: other, Length: 2, Rest: []byte("ok")})); err != nil {
				t.Fatal(err)
			}
			var echo [2]byte
			if _, err := io.ReadFull(sibling, echo[:]); err != nil || string(echo[:]) != "ok" {
				t.Fatalf("sibling %q: %v", echo, err)
			}
			select {
			case err := <-closed:
				if err != context.DeadlineExceeded || time.Since(start) < 5*time.Second {
					t.Fatalf("Close after %s: %v", time.Since(start), err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("Close still blocked after its five-second budget")
			}
			assertNoOwner(t, "child")
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			clear(data)
			if m.chanList.getChan(id) != ch.Channel {
				t.Fatal("ID released before physical write")
			}
			if action == "resume" {
				close(wire.resume)
				var msg channelDataMsg
				p, err := peer.readPacket()
				if err != nil || Unmarshal(p, &msg) != nil || string(msg.Rest) != "accepted data" || msg.PeersID != id+10 {
					t.Fatalf("pending DATA: %x, %v", p, err)
				}
				if err := ch.Close(); err != nil {
					t.Fatal(err)
				}
				p, err = peer.readPacket()
				var end channelCloseMsg
				if err != nil || Unmarshal(p, &end) != nil || end.PeersID != id+10 {
					t.Fatalf("CLOSE: %x, %v", p, err)
				}
				if m.chanList.getChan(id) != ch.Channel {
					t.Fatal("ID released before peer CLOSE")
				}
				if err := peer.writePacket(Marshal(channelCloseMsg{PeersID: id})); err != nil {
					t.Fatal(err)
				}
				if err := peer.writePacket(Marshal(channelDataMsg{PeersID: other, Length: 2, Rest: []byte("OK")})); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(sibling, echo[:]); err != nil || string(echo[:]) != "OK" {
					t.Fatalf("sibling after recovery: %q, %v", echo, err)
				}
				if m.chanList.getChan(id) != nil {
					t.Fatal("completed ID still reserved")
				}
				if _, err = sibling.Write([]byte("after")); err != nil {
					t.Fatal(err)
				}
				p, err = peer.readPacket()
				if err != nil || Unmarshal(p, &msg) != nil || msg.PeersID != other+10 || string(msg.Rest) != "after" {
					t.Fatalf("unexpected packet after CLOSE: %x, %v", p, err)
				}
			}
			m.Close()
			select {
			case <-m.forwardWrites.done:
			default:
				t.Fatal("parent Close returned before writer exit")
			}
			m.Wait()
			assertNoOwner(t, "parent")
			if action == "disconnect" && ch.Close() == nil {
				t.Fatal("unsent CLOSE reported success")
			}
		})
	}
}

type failNextSocketWrite struct {
	net.Conn
	armed atomic.Bool
	err   error
}

func TestForwardCloseNotAcknowledgedInKeyExchange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		local, peer := memPipe()
		conf := new(ServerConfig)
		conf.AddHostKey(testSigners["ecdsa"])
		conf.SetDefaults()
		hs := newServerTransport(&errorKeyingTransport{local, -1, -1}, []byte("client"), []byte("server"), conf)
		defer hs.Close()
		p, err := peer.readPacket()
		if err != nil || p[0] != msgKexInit {
			t.Fatalf("expected key exchange, got %x: %v", p, err)
		}
		var writer forwardWriter
		writer.start(hs)
		defer writer.stop(io.EOF)
		result := make(chan error, 1)
		if err := writer.put(Marshal(channelCloseMsg{PeersID: 1}), func(err error) { result <- err }); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case err := <-result:
			t.Fatalf("CLOSE completed during key exchange: %v", err)
		default:
		}
		peer.Close()
		if err := <-result; !errors.Is(err, io.EOF) {
			t.Fatalf("unsent CLOSE: %v", err)
		}
		<-writer.done
	})
}

func (c *failNextSocketWrite) Write(p []byte) (int, error) {
	if c.armed.Swap(false) {
		return 0, c.err
	}
	return c.Conn.Write(p)
}

func TestForwardCloseReturnsFirstWriteError(t *testing.T) {
	server, local, err := netPipe()
	if err != nil {
		t.Fatal(err)
	}
	wire := &failNextSocketWrite{Conn: local, err: errors.New("socket write failed")}
	client, _, incoming := forwardingPairOn(t, server, wire)
	conn, err := client.Dial("tcp", "localhost:22")
	if err != nil {
		t.Fatal(err)
	}
	peer := <-incoming
	native := conn.(*chanConn).Channel.(*channel)

	// A following byte on a sibling orders the peer CLOSE before the ID check.
	sibling, err := client.Dial("tcp", "localhost:23")
	if err != nil {
		t.Fatal(err)
	}
	peerSibling := <-incoming
	wire.armed.Store(true)
	if err := conn.Close(); err != wire.err {
		t.Errorf("first CLOSE returned %v, want original socket error", err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := peerSibling.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := io.ReadFull(sibling, b[:]); err != nil || b[0] != 'x' {
		t.Fatalf("peer barrier: %q, %v", b, err)
	}
	if native.mux.chanList.getChan(native.localId) != native {
		t.Fatal("channel ID released despite failed local CLOSE")
	}
}

// The peer can withhold individual replies without stopping sibling traffic.
func openTestForward(t *testing.T, client *Client, peer packetConn) (*chanConn, uint32) {
	t.Helper()
	type result struct {
		ch  *chanConn
		err error
	}
	opened := make(chan result, 1)
	go func() {
		conn, err := client.Dial("tcp", "localhost:22")
		ch, _ := conn.(*chanConn)
		opened <- result{ch, err}
	}()
	p, err := peer.readPacket()
	if err != nil {
		t.Fatal(err)
	}
	var open channelOpenMsg
	if err := Unmarshal(p, &open); err != nil {
		t.Fatal(err)
	}
	if err := peer.writePacket(Marshal(channelOpenConfirmMsg{PeersID: open.PeersID, MyID: open.PeersID + 10, MyWindow: channelWindowSize, MaxPacketSize: channelMaxPacket})); err != nil {
		t.Fatal(err)
	}
	got := <-opened
	if got.err != nil {
		t.Fatal(got.err)
	}
	return got.ch, open.PeersID
}

func TestForwardCloseWakesRequest(t *testing.T) {
	local, peer := memPipe()
	m := newMux(local)
	defer m.Close()
	ch, id := openTestForward(t, &Client{Conn: &connection{mux: m}}, peer)
	result := make(chan error, 1)
	go func() { _, err := ch.SendRequest("keepalive", true, nil); result <- err }()
	p, err := peer.readPacket()
	if err != nil || p[0] != msgChannelRequest {
		t.Fatalf("expected request: %x, %v", p, err)
	}
	if err := ch.Close(); err != nil {
		t.Fatal(err)
	}
	if err := peer.writePacket(Marshal(channelRequestSuccessMsg{PeersID: id})); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("closed request: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request still waiting after local Close")
	}
}

type blockedDataWriter struct {
	packetConn
	started, resume chan struct{}
}

func (w *blockedDataWriter) writePacket(p []byte) error {
	if p[0] == msgChannelData {
		close(w.started)
		<-w.resume
	}
	return w.packetConn.writePacket(p)
}

func TestForwardCrossedCloseDoesNotBlockSibling(t *testing.T) {
	local, peer := memPipe()
	wire := &blockedDataWriter{local, make(chan struct{}), make(chan struct{})}
	m := newMux(wire)
	defer m.Close()
	release := sync.OnceFunc(func() { close(wire.resume) })
	defer release()
	client := &Client{Conn: &connection{mux: m}}
	ch, id := openTestForward(t, client, peer)
	sibling, other := openTestForward(t, client, peer)
	written := make(chan error, 1)
	go func() { _, err := ch.Write([]byte("in flight")); written <- err }()
	<-wire.started
	closed := make(chan error, 1)
	go func() { closed <- ch.Close() }()
	<-ch.Channel.(*channel).forwardDone
	if err := peer.writePacket(Marshal(channelCloseMsg{PeersID: id})); err != nil {
		t.Fatal(err)
	}
	if err := peer.writePacket(Marshal(channelDataMsg{PeersID: other, Length: 2, Rest: []byte("ok")})); err != nil {
		t.Fatal(err)
	}
	read := make(chan string, 1)
	go func() {
		var b [2]byte
		_, err := io.ReadFull(sibling, b[:])
		if err != nil {
			read <- err.Error()
			return
		}
		read <- string(b[:])
	}()
	select {
	case got := <-read:
		if got != "ok" {
			t.Fatalf("sibling received %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("crossed CLOSE blocked the sibling's incoming data")
	}
	if m.chanList.getChan(id) != ch.Channel {
		t.Fatal("channel ID released before the local CLOSE could be written")
	}
	release()
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if m.chanList.getChan(id) != nil {
		t.Fatal("channel ID retained after both CLOSE packets")
	}
	for _, want := range []byte{msgChannelData, msgChannelClose} {
		p, err := peer.readPacket()
		if err != nil || p[0] != want {
			t.Fatalf("wanted packet %d, got %x (%v)", want, p, err)
		}
	}
}
