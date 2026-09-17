package peer

import (
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	frameLimit = 16 * 1024
	windowSize = 64 * 1024
)

type channelWire interface {
	Send([]byte) error
	SendText(string) error
	Close() error
	BufferedAmount() uint64
}

type peerAddr string

func (a peerAddr) Network() string { return "webrtc" }
func (a peerAddr) String() string  { return string(a) }

type rateLimit struct {
	tokens float64
	at     time.Time
}

func (r *rateLimit) allow(now time.Time, rate, burst float64) bool {
	if r.at.IsZero() {
		r.tokens = burst
	} else {
		r.tokens = min(burst, r.tokens+now.Sub(r.at).Seconds()*rate)
	}
	r.at = now
	if r.tokens < 1 {
		return false
	}
	r.tokens--
	return true
}

// stream is a bounded byte stream. SCTP ordering is supplemented by application
// credit so a stalled HTTP consumer cannot turn into an unbounded SCTP queue.
type stream struct {
	wire       channelWire
	peerID     string
	remote     net.TCPAddr
	done       func()
	doneOnce   sync.Once
	mu         sync.Mutex
	writeMu    sync.Mutex
	sendMu     sync.Mutex
	wake       chan struct{}
	buffer     [windowSize]byte
	head       int
	size       int
	credit     int
	recvCredit int
	err        error
	closed     bool
	wireDone   bool
	sentFin    bool
	gotFin     bool
	finAck     bool
	readBy     time.Time
	writeBy    time.Time
	rate       rateLimit
}

var _ net.Conn = (*stream)(nil)

func newStream(w channelWire, id string, remote net.TCPAddr, done func()) *stream {
	return &stream{wire: w, peerID: id, remote: remote, credit: windowSize, recvCredit: windowSize, wake: make(chan struct{}), done: done}
}

func (s *stream) notifyLocked() {
	close(s.wake)
	s.wake = make(chan struct{})
}

func (s *stream) finish() {
	s.doneOnce.Do(func() {
		if s.done != nil {
			s.done()
		}
	})
}

func (s *stream) abort(err error) {
	if err == nil {
		err = net.ErrClosed
	}
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.wireDone = true
	s.notifyLocked()
	s.mu.Unlock()
	_ = s.wire.Close()
	s.finish()
}

func (s *stream) transportClosed() {
	s.mu.Lock()
	s.wireDone = true
	if !s.gotFin && s.err == nil {
		s.err = io.ErrUnexpectedEOF
	}
	s.notifyLocked()
	s.mu.Unlock()
	s.finish()
}

func (s *stream) send(data []byte, text bool) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.wire.BufferedAmount() > windowSize*4 {
		return errors.New("data channel send queue limit")
	}
	if text {
		return s.wire.SendText(string(data))
	}
	return s.wire.Send(data)
}

func (s *stream) grantCredit(n int) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.wire.BufferedAmount() > windowSize*4 {
		return errors.New("data channel send queue limit")
	}
	s.mu.Lock()
	if s.gotFin || s.wireDone || s.err != nil {
		s.mu.Unlock()
		return nil
	}
	s.recvCredit += n
	s.mu.Unlock()
	return s.wire.SendText("credit:" + strconv.Itoa(n))
}

func (s *stream) receive(data []byte, text bool) {
	var reply string
	s.mu.Lock()
	if s.err != nil || s.wireDone {
		s.mu.Unlock()
		return
	}
	var err error
	if !s.rate.allow(time.Now(), 8192, 16384) {
		err = errors.New("stream frame rate exceeded")
	} else if !text {
		if len(data) == 0 || len(data) > frameLimit || len(data) > s.recvCredit || len(data) > windowSize-s.size || s.gotFin {
			err = errors.New("invalid stream data or exceeded receive credit")
		} else {
			tail := (s.head + s.size) % windowSize
			n := copy(s.buffer[tail:], data)
			copy(s.buffer[:], data[n:])
			s.size += len(data)
			s.recvCredit -= len(data)
		}
	} else if len(data) > 32 {
		err = errors.New("oversized stream control")
	} else {
		switch message := string(data); {
		case message == "fin":
			if s.gotFin {
				err = errors.New("duplicate fin")
			} else {
				s.gotFin = true
				reply = "fin-ack"
			}
		case message == "fin-ack":
			if !s.sentFin || s.finAck {
				err = errors.New("unexpected fin acknowledgement")
			} else {
				s.finAck = true
			}
		case strings.HasPrefix(message, "credit:"):
			value := strings.TrimPrefix(message, "credit:")
			n, parseErr := strconv.Atoi(value)
			if parseErr != nil || n <= 0 || n > windowSize || strconv.Itoa(n) != value || n > windowSize-s.credit {
				err = errors.New("invalid stream credit")
			} else {
				s.credit += n
			}
		default:
			err = errors.New("unknown stream control")
		}
	}
	s.notifyLocked()
	s.mu.Unlock()
	if err != nil {
		s.abort(err)
		return
	}
	if reply != "" {
		if err = s.send([]byte(reply), true); err != nil {
			s.abort(err)
		}
	}
}

func expired(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}

func waitStream(wake <-chan struct{}, deadline time.Time) error {
	if deadline.IsZero() {
		<-wake
		return nil
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-wake:
		return nil
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}

func (s *stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		s.mu.Lock()
		if expired(s.readBy) {
			s.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		if s.err != nil || s.closed {
			err := s.err
			if err == nil {
				err = net.ErrClosed
			}
			s.mu.Unlock()
			return 0, err
		}
		if s.size > 0 {
			n := min(len(p), s.size)
			first := copy(p[:n], s.buffer[s.head:min(windowSize, s.head+n)])
			copy(p[first:n], s.buffer[:n-first])
			s.head = (s.head + n) % windowSize
			s.size -= n
			// A FIN already proves the sender will not need further credit.
			sendCredit := !s.gotFin && !s.wireDone
			s.mu.Unlock()
			if sendCredit {
				if err := s.grantCredit(n); err != nil {
					s.abort(err)
					return n, err
				}
			}
			return n, nil
		}
		if s.gotFin {
			s.mu.Unlock()
			return 0, io.EOF
		}
		wake, deadline := s.wake, s.readBy
		s.mu.Unlock()
		if err := waitStream(wake, deadline); err != nil {
			// A deadline may have been extended while this timer fired.
			s.mu.Lock()
			stillExpired := expired(s.readBy)
			s.mu.Unlock()
			if stillExpired {
				return 0, err
			}
		}
	}
}

func (s *stream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	written := 0
	for len(p) > 0 {
		s.mu.Lock()
		if expired(s.writeBy) {
			s.mu.Unlock()
			return written, os.ErrDeadlineExceeded
		}
		if s.err != nil || s.closed || s.sentFin || s.wireDone {
			err := s.err
			if err == nil {
				err = net.ErrClosed
			}
			s.mu.Unlock()
			return written, err
		}
		if s.credit > 0 {
			n := min(len(p), frameLimit, s.credit)
			s.credit -= n
			s.mu.Unlock()
			if err := s.send(p[:n], false); err != nil {
				s.abort(err)
				return written, err
			}
			p = p[n:]
			written += n
			continue
		}
		wake, deadline := s.wake, s.writeBy
		s.mu.Unlock()
		if err := waitStream(wake, deadline); err != nil {
			s.mu.Lock()
			stillExpired := expired(s.writeBy)
			s.mu.Unlock()
			if stillExpired {
				return written, err
			}
		}
	}
	return written, nil
}

func (s *stream) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	if s.sentFin {
		s.mu.Unlock()
		return nil
	}
	if s.err != nil || s.wireDone {
		err := s.err
		if err == nil {
			err = net.ErrClosed
		}
		s.mu.Unlock()
		return err
	}
	s.sentFin = true
	s.notifyLocked()
	s.mu.Unlock()
	if err := s.send([]byte("fin"), true); err != nil {
		s.abort(err)
		return err
	}
	return nil
}

// Close flushes the ordered FIN handshake, rather than resetting SCTP while an
// HTTP server's final response is still in flight. Abort paths never wait.
func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	deadline := time.Now().Add(5 * time.Second)
	if !s.writeBy.IsZero() && s.writeBy.Before(deadline) {
		deadline = s.writeBy
	}
	s.notifyLocked()
	s.mu.Unlock()
	if err := s.CloseWrite(); err != nil {
		s.abort(err)
		return err
	}
	for {
		s.mu.Lock()
		if s.finAck || s.wireDone || s.err != nil {
			s.wireDone = true
			s.notifyLocked()
			s.mu.Unlock()
			err := s.wire.Close()
			s.finish()
			return err
		}
		wake := s.wake
		s.mu.Unlock()
		if err := waitStream(wake, deadline); err != nil {
			s.abort(err)
			return err
		}
	}
}

func (s *stream) PeerID() string      { return s.peerID }
func (s *stream) LocalAddr() net.Addr { return peerAddr("peer.shareme") }
func (s *stream) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: append(net.IP(nil), s.remote.IP...), Port: s.remote.Port}
}
func (s *stream) SetDeadline(t time.Time) error {
	s.mu.Lock()
	s.readBy, s.writeBy = t, t
	s.notifyLocked()
	s.mu.Unlock()
	return nil
}
func (s *stream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.readBy = t
	s.notifyLocked()
	s.mu.Unlock()
	return nil
}
func (s *stream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	s.writeBy = t
	s.notifyLocked()
	s.mu.Unlock()
	return nil
}

type listener struct {
	queue chan *stream
	done  chan struct{}
	once  sync.Once
}

func newListener() *listener {
	return &listener{queue: make(chan *stream, 32), done: make(chan struct{})}
}

func (l *listener) Addr() net.Addr { return peerAddr("peer.shareme") }
func (l *listener) Accept() (net.Conn, error) {
	for {
		select {
		case <-l.done:
			return nil, net.ErrClosed
		default:
		}
		select {
		case <-l.done:
			return nil, net.ErrClosed
		case s := <-l.queue:
			s.mu.Lock()
			valid := s.err == nil && !s.closed
			s.mu.Unlock()
			if valid {
				return s, nil
			}
		}
	}
}
func (l *listener) offer(s *stream) {
	select {
	case <-l.done:
		s.abort(net.ErrClosed)
		return
	default:
	}
	select {
	case l.queue <- s:
	case <-l.done:
		s.abort(net.ErrClosed)
	default:
		s.abort(errors.New("HTTP listener backlog full"))
	}
}
func (l *listener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}
