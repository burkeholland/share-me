package peer

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testWire struct {
	remote *stream
	closed atomic.Bool
	mu     sync.Mutex
	texts  []string
	bytes  int
}

func (w *testWire) Send(data []byte) error {
	if w.closed.Load() {
		return net.ErrClosed
	}
	w.mu.Lock()
	w.bytes += len(data)
	w.mu.Unlock()
	if w.remote != nil {
		w.remote.receive(data, false)
	}
	return nil
}
func (w *testWire) SendText(data string) error {
	if w.closed.Load() {
		return net.ErrClosed
	}
	w.mu.Lock()
	w.texts = append(w.texts, data)
	w.mu.Unlock()
	if w.remote != nil {
		w.remote.receive([]byte(data), true)
	}
	return nil
}
func (w *testWire) Close() error {
	if !w.closed.Swap(true) && w.remote != nil {
		w.remote.transportClosed()
	}
	return nil
}
func (*testWire) BufferedAmount() uint64 { return 0 }

func streamPair() (*stream, *stream) {
	wa, wb := &testWire{}, &testWire{}
	addr := net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
	a, b := newStream(wa, "device", addr, nil), newStream(wb, "device", addr, nil)
	wa.remote, wb.remote = b, a
	return a, b
}

func TestStreamBackpressureAndGracefulClose(t *testing.T) {
	a, b := streamPair()
	t.Cleanup(func() { a.abort(net.ErrClosed); b.abort(net.ErrClosed) })
	_ = a.SetWriteDeadline(time.Now().Add(5 * time.Second))
	payload := bytes.Repeat([]byte("0123456789abcdef"), windowSize/8)
	sent := make(chan error, 1)
	go func() {
		_, err := a.Write(payload)
		if err == nil {
			err = a.Close()
		}
		sent <- err
	}()
	waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.size == windowSize })
	select {
	case err := <-sent:
		t.Fatalf("writer failed to backpressure: %v", err)
	default:
	}
	head := make([]byte, windowSize)
	if _, err := io.ReadFull(b, head); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	// The underlying channel is closed, but ordered response bytes survive.
	tail, err := io.ReadAll(b)
	if err != nil || !bytes.Equal(append(head, tail...), payload) {
		t.Fatalf("response truncated on close: %d %v", len(tail), err)
	}
	if b.PeerID() != "device" || b.LocalAddr().String() != "peer.shareme" {
		t.Fatal("connection attribution missing")
	}
	if _, ok := b.RemoteAddr().(*net.TCPAddr); !ok {
		t.Fatal("remote address is not TCPAddr")
	}
}

func TestStreamMaliciousFrames(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		text bool
	}{
		{"oversized", make([]byte, frameLimit+1), false},
		{"empty", nil, false},
		{"inflated-credit", []byte("credit:1"), true},
		{"negative-credit", []byte("credit:-1"), true},
		{"zero-credit", []byte("credit:0"), true},
		{"noncanonical-credit", []byte("credit:01"), true},
		{"overflow-credit", []byte("credit:9999999999999999999999999"), true},
		{"unknown-control", []byte("invoke:Delete"), true},
		{"oversized-control", make([]byte, 256), true},
		{"unsolicited-fin-ack", []byte("fin-ack"), true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			w := &testWire{}
			s := newStream(w, "", net.TCPAddr{}, nil)
			s.receive(test.data, test.text)
			if !w.closed.Load() {
				t.Fatal("malicious frame accepted")
			}
		})
	}
	t.Run("receive-window", func(t *testing.T) {
		w := &testWire{}
		s := newStream(w, "", net.TCPAddr{}, nil)
		for i := 0; i < 4; i++ {
			s.receive(make([]byte, frameLimit), false)
		}
		s.receive([]byte("x"), false)
		if !w.closed.Load() || s.size != windowSize {
			t.Fatal("receive window exceeded")
		}
	})
	t.Run("data-after-fin", func(t *testing.T) {
		w := &testWire{}
		s := newStream(w, "", net.TCPAddr{}, nil)
		s.receive([]byte("fin"), true)
		s.receive([]byte("x"), false)
		if !w.closed.Load() {
			t.Fatal("data after fin accepted")
		}
	})
}

func TestStreamDeadlinesAndCancellation(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		a, b := streamPair()
		defer b.abort(net.ErrClosed)
		_ = a.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
		_, err := a.Read(make([]byte, 1))
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("read deadline: %v", err)
		}
		_ = a.SetReadDeadline(time.Time{})
		if _, err := b.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 1)
		if _, err := a.Read(buffer); err != nil || buffer[0] != 'x' {
			t.Fatalf("read deadline permanently closed the stream: %v", err)
		}
	})
	t.Run("write", func(t *testing.T) {
		a, b := streamPair()
		defer b.abort(net.ErrClosed)
		_ = a.SetWriteDeadline(time.Now().Add(30 * time.Millisecond))
		n, err := a.Write(make([]byte, windowSize+1))
		if !errors.Is(err, os.ErrDeadlineExceeded) || n != windowSize {
			t.Fatalf("write deadline: %d %v", n, err)
		}
		if _, err := b.Read(make([]byte, windowSize)); err != nil {
			t.Fatal(err)
		}
		_ = a.SetWriteDeadline(time.Time{})
		if _, err := a.Write([]byte("x")); err != nil {
			t.Fatalf("write deadline permanently closed the stream: %v", err)
		}
	})
	t.Run("deadline-update", func(t *testing.T) {
		a, b := streamPair()
		defer b.abort(net.ErrClosed)
		done := make(chan error, 1)
		go func() { _, err := a.Read(make([]byte, 1)); done <- err }()
		_ = a.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
		if err := <-done; !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("updated read deadline: %v", err)
		}
	})
	t.Run("close-unblocks", func(t *testing.T) {
		a, b := streamPair()
		defer b.abort(net.ErrClosed)
		read, write := make(chan error, 1), make(chan error, 1)
		go func() { _, err := a.Read(make([]byte, 1)); read <- err }()
		go func() { _, err := a.Write(make([]byte, windowSize+1)); write <- err }()
		waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.size == windowSize })
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-read; !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
		if err := <-write; !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	})
}

func TestStreamCreditOnlyAfterConsumerRead(t *testing.T) {
	w := &testWire{}
	s := newStream(w, "", net.TCPAddr{}, nil)
	for i := 0; i < 4; i++ {
		s.receive(make([]byte, frameLimit), false)
	}
	w.mu.Lock()
	credits := len(w.texts)
	w.mu.Unlock()
	if credits != 0 {
		t.Fatal("credit returned before the consumer read")
	}
	s.sendMu.Lock()
	read := make(chan error, 1)
	go func() { _, err := s.Read(make([]byte, 16)); read <- err }()
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.size == windowSize-16 })
	s.receive([]byte("x"), false)
	s.sendMu.Unlock()
	<-read
	if !w.closed.Load() {
		t.Fatal("data accepted before pending consumer credit was issued")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
