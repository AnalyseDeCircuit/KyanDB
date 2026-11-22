package ssh

import (
	"encoding/binary"
	"io"
	"os"
	"sync"
	"time"
)

type forwardPacket struct {
	data     []byte
	complete func(error)
}

// The mux owns accepted packets until the transport actually writes them.
// Each forward admits at most one outstanding DATA packet and one CLOSE.
type forwardWriter struct {
	mu      sync.Mutex
	ready   *sync.Cond
	queue   []forwardPacket
	stopped error
	done    chan struct{}
}

func (w *forwardWriter) start(conn packetConn) {
	w.ready = sync.NewCond(&w.mu)
	w.done = make(chan struct{})
	write := conn.writePacket
	if hs, ok := conn.(*handshakeTransport); ok {
		write = func(p []byte) error { return hs.writePacketMode(p, false) }
	}
	go func() {
		defer close(w.done)
		for {
			w.mu.Lock()
			for len(w.queue) == 0 && w.stopped == nil {
				w.ready.Wait()
			}
			if w.stopped != nil {
				left, err := w.queue, w.stopped
				w.queue = nil
				w.mu.Unlock()
				for _, p := range left {
					p.complete(err)
				}
				return
			}
			p := w.queue[0]
			w.queue[0] = forwardPacket{}
			w.queue = w.queue[1:]
			w.mu.Unlock()
			err := write(p.data)
			if err != nil {
				w.stop(err)
			}
			p.complete(err)
		}
	}()
}

func (w *forwardWriter) put(p []byte, complete func(error)) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped != nil {
		return w.stopped
	}
	w.queue = append(w.queue, forwardPacket{p, complete})
	w.ready.Signal()
	return nil
}

func (w *forwardWriter) stop(err error) {
	w.mu.Lock()
	if w.stopped == nil {
		w.stopped = err
		w.ready.Signal()
	}
	w.mu.Unlock()
}

// The condition's lock protects the deadline. A stale timer may wake a
// waiter, but only the current deadline determines whether it times out.
type ioDeadline struct {
	at    time.Time
	timer *time.Timer
}

func (d *ioDeadline) setLocked(cond *sync.Cond, at time.Time) {
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.at = at
	if delay := time.Until(at); !at.IsZero() && delay > 0 {
		d.timer = time.AfterFunc(delay, func() { cond.L.Lock(); cond.Broadcast(); cond.L.Unlock() })
	}
	cond.Broadcast()
}

func (d *ioDeadline) expired() bool { return !d.at.IsZero() && !time.Now().Before(d.at) }

func (f *forwardCloser) writeControl(ch *channel, p []byte) error {
	// As with DATA, backpressure synchronous control requests instead of
	// accumulating one queued packet per caller. CLOSE bypasses this lock.
	if p[0] != msgChannelWindowAdjust {
		ch.writeMu.Lock()
		defer ch.writeMu.Unlock()
	}
	w := &ch.remoteWin
	w.L.Lock()
	if w.closed {
		w.L.Unlock()
		return io.EOF
	}
	if p[0] == msgChannelEOF {
		f.eof = true
		w.Broadcast()
	}
	// Window credit is bounded by data already received. A blocked socket
	// must not prevent delivery of that data to a sibling reader.
	if p[0] == msgChannelWindowAdjust {
		err := ch.mux.forwardWrites.put(p, func(error) {})
		w.L.Unlock()
		return err
	}
	var wireErr error
	done := make(chan struct{})
	err := ch.mux.forwardWrites.put(p, func(err error) { wireErr = err; close(done) })
	w.L.Unlock()
	if err != nil {
		return err
	}
	select {
	case <-done:
		return wireErr
	case <-ch.forwardDone:
		return io.EOF
	}
}

func (f *forwardCloser) writeData(ch *channel, b []byte, extended uint32) (n int, err error) {
	w := &ch.remoteWin
	w.L.Lock()
	defer w.L.Unlock()
	for len(b) > 0 {
		for !w.closed && !f.eof && !f.deadline.expired() && (w.win == 0 || f.dataPending) {
			w.Wait()
		}
		if w.closed || f.eof {
			return n, io.EOF
		}
		if f.deadline.expired() {
			return n, os.ErrDeadlineExceeded
		}
		size := int(minPayloadSize(min(w.win, ch.maxRemotePayload, channelMaxPacket), len(b)))
		header := 9
		if extended != 0 {
			header = 13
		}
		p := make([]byte, header+size)
		p[0] = msgChannelData
		if extended != 0 {
			p[0] = msgChannelExtendedData
			binary.BigEndian.PutUint32(p[5:], extended)
		}
		binary.BigEndian.PutUint32(p[1:], ch.remoteId)
		binary.BigEndian.PutUint32(p[header-4:], uint32(size))
		copy(p[header:], b[:size])
		var completed bool
		var writeErr error
		err := ch.mux.forwardWrites.put(p, func(err error) {
			w.L.Lock()
			completed, writeErr = true, err
			f.dataPending = false
			w.Broadcast()
			w.L.Unlock()
		})
		if err != nil {
			return n, err
		}
		w.win -= uint32(size)
		f.dataPending = true
		for !completed && !w.closed && !f.deadline.expired() {
			w.Wait()
		}
		if completed && writeErr != nil {
			return n, writeErr
		}
		// Even if interrupted, this prefix belongs to the parent and will
		// not be taken from the caller's buffer or sent a second time.
		n += size
		b = b[size:]
		if !completed && f.deadline.expired() {
			return n, os.ErrDeadlineExceeded
		}
	}
	return n, nil
}
