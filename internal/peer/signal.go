package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type signalSocket struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *signalSocket) write(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil || len(data) > maxSignal {
		return errors.New("outgoing signaling message too large")
	}
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (e *Engine) signalLoop() {
	backoff := time.Second
	for e.ctx.Err() == nil {
		started := time.Now()
		err := e.signalSession()
		if e.ctx.Err() != nil {
			return
		}
		e.setStatus(false, fmt.Sprintf("Signaling unavailable; reconnecting in %s", backoff))
		e.report(fmt.Errorf("signaling connection failed: %w", err))
		timer := time.NewTimer(backoff)
		select {
		case <-e.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		} else {
			backoff = min(30*time.Second, backoff*2)
		}
	}
}

func (e *Engine) signalSession() error {
	u, _ := url.Parse(e.origin)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/signal/" + e.room
	dialer := websocket.Dialer{
		Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 10 * time.Second,
		ReadBufferSize: 4096, WriteBufferSize: 4096, EnableCompression: false,
	}
	conn, response, err := dialer.DialContext(e.ctx, u.String(), nil)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return err
	}
	defer conn.Close()
	conn.SetReadLimit(maxSignal)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return net.ErrClosed
	}
	e.socket = conn
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		if e.socket == conn {
			e.socket = nil
		}
		e.mu.Unlock()
	}()
	socket := &signalSocket{conn: conn}
	kind, data, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	var challenge struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if kind != websocket.TextMessage || strictJSON(data, &challenge, maxSignal) != nil || challenge.Type != "challenge" {
		return errors.New("invalid broker challenge")
	}
	signature, err := signChallenge(e.key, e.room, challenge.Challenge)
	if err != nil {
		return err
	}
	if err = socket.write(struct {
		Type      string `json:"type"`
		PublicKey string `json:"publicKey"`
		Signature string `json:"signature"`
	}{"host", raw64.EncodeToString(publicBytes(e.key)), signature}); err != nil {
		return err
	}
	kind, data, err = conn.ReadMessage()
	if err != nil {
		return err
	}
	var ready struct {
		Type string `json:"type"`
	}
	if kind != websocket.TextMessage || strictJSON(data, &ready, maxSignal) != nil || ready.Type != "host-ready" {
		return errors.New("broker rejected host identity")
	}
	e.setStatus(true, "Signaling connected")
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	pingDone, pingStopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(pingStopped)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	defer func() { close(pingDone); <-pingStopped }()
	for {
		kind, data, err = conn.ReadMessage()
		if err != nil {
			return err
		}
		if kind != websocket.TextMessage {
			return errors.New("binary signaling prohibited")
		}
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		var env envelope
		if err = strictJSON(data, &env, maxSignal); err != nil || validateEnvelope(env) != nil || env.Kind != "offer" {
			return errors.New("invalid broker signaling message")
		}
		if err = e.handleOffer(env, socket.write); err != nil {
			e.report(err)
		}
	}
}

func (e *Engine) handleOffer(env envelope, reply func(any) error) error {
	if err := validateEnvelope(env); err != nil || env.Kind != "offer" {
		return errors.New("invalid offer envelope")
	}
	e.mu.Lock()
	if e.closed || !e.started || len(e.peers) >= 8 || len(e.seen) >= 65536 ||
		!e.signalRate.allow(time.Now(), 4, 16) {
		e.mu.Unlock()
		return errors.New("peer or signaling rate limit reached")
	}
	if _, duplicate := e.seen[env.SID]; duplicate {
		e.mu.Unlock()
		return errors.New("duplicate signaling session")
	}
	var key []byte
	generation := e.inviteGen
	if env.KID == "pair" {
		key = append([]byte(nil), e.invite...)
	} else if device, ok := e.state.Devices[env.KID]; ok {
		key = append([]byte(nil), device.Secret...)
	}
	e.mu.Unlock()
	if len(key) != 32 {
		return errors.New("this connection is no longer paired. Choose Add phone and scan the new QR code")
	}
	plain, err := openSignal(key, e.room, env)
	if err != nil {
		clear(key)
		return err
	}
	var proposed offer
	err = strictJSON(plain, &proposed, maxSignal)
	clear(plain)
	if err != nil || len(proposed.SDP) == 0 || len(proposed.SDP) > 24*1024 || !validName(proposed.Name) {
		clear(key)
		return errors.New("invalid authenticated offer")
	}
	e.mu.Lock()
	_, exists := e.state.Devices[env.KID]
	_, duplicate := e.seen[env.SID]
	if e.closed || len(e.peers) >= 8 || duplicate ||
		(env.KID == "pair" && (generation != e.inviteGen || len(e.invite) != 32)) ||
		(env.KID != "pair" && !exists) {
		e.mu.Unlock()
		clear(key)
		return errors.New("offer authorization changed")
	}
	p := &remotePeer{
		engine: e, sid: env.SID, kid: env.KID, name: proposed.Name, inviteGen: generation,
		streams: map[string]*stream{},
		done:    make(chan struct{}), opened: make(chan struct{}),
		approved: make(chan struct{}), authorized: make(chan struct{}),
	}
	if env.KID != "pair" {
		p.deviceID = env.KID
	}
	p.ctx, p.cancel = context.WithCancel(e.ctx)
	e.peers[env.SID] = p
	e.seen[env.SID] = struct{}{}
	e.wg.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.wg.Done()
		defer clear(key)
		defer func() {
			p.shutdown()
			e.mu.Lock()
			pc := p.pc
			e.mu.Unlock()
			if pc != nil {
				_ = pc.GracefulClose()
			}
		}()
		if err := p.negotiate(proposed.SDP, key, reply); err != nil {
			p.shutdown()
			if e.ctx.Err() == nil {
				e.report(err)
			}
			return
		}
		clear(key)
		p.lifecycle()
	}()
	return nil
}

func validHTTPLabel(label string) bool {
	const prefix = "shareme.http."
	return strings.HasPrefix(label, prefix) && validID(strings.TrimPrefix(label, prefix))
}
