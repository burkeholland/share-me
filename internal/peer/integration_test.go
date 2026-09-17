package peer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

type testBroker struct {
	server  *httptest.Server
	hosts   chan *signalSocket
	answers chan envelope
	errors  chan error
	offline atomic.Bool
}

func newTestBroker(t *testing.T) *testBroker {
	t.Helper()
	b := &testBroker{hosts: make(chan *signalSocket, 8), answers: make(chan envelope, 8), errors: make(chan error, 8)}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			b.errors <- err
			return
		}
		defer conn.Close()
		conn.SetReadLimit(maxSignal)
		socket := &signalSocket{conn: conn}
		challenge := raw64.EncodeToString(bytes.Repeat([]byte{7}, 32))
		if err := socket.write(map[string]string{"type": "challenge", "challenge": challenge}); err != nil {
			b.errors <- err
			return
		}
		kind, data, err := conn.ReadMessage()
		var host struct {
			Type      string `json:"type"`
			PublicKey string `json:"publicKey"`
			Signature string `json:"signature"`
		}
		if err != nil || kind != websocket.TextMessage || strictJSON(data, &host, maxSignal) != nil || host.Type != "host" {
			b.errors <- fmt.Errorf("invalid native host authentication: %v", err)
			return
		}
		public, err := decodeFixed(host.PublicKey, 65)
		if err != nil {
			b.errors <- err
			return
		}
		x, y := elliptic.Unmarshal(elliptic.P256(), public)
		sig, err := decodeFixed(host.Signature, 64)
		room := strings.TrimPrefix(r.URL.Path, "/signal/")
		hash := sha256.Sum256([]byte("ShareMe host v1\n" + room + "\n" + challenge))
		roomHash := sha256.Sum256(public)
		if x == nil || err != nil || fmt.Sprintf("%x", roomHash[:16]) != room ||
			!ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, hash[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			b.errors <- fmt.Errorf("host proof verification failed")
			return
		}
		if err := socket.write(map[string]string{"type": "host-ready"}); err != nil {
			b.errors <- err
			return
		}
		b.hosts <- socket
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var answer envelope
			if kind != websocket.TextMessage || strictJSON(data, &answer, maxSignal) != nil || answer.Kind != "answer" {
				b.errors <- fmt.Errorf("invalid encrypted answer")
				return
			}
			b.answers <- answer
		}
	}))
	t.Cleanup(b.server.Close)
	return b
}

type testGuest struct {
	pc       *webrtc.PeerConnection
	control  *webrtc.DataChannel
	messages chan controlMessage
}

func connectGuest(t *testing.T, e *Engine, broker *testBroker, host *signalSocket, kid string, key []byte) *testGuest {
	t.Helper()
	pc, err := e.newPeerConnection()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	control, err := pc.CreateDataChannel("shareme.control", nil)
	if err != nil {
		t.Fatal(err)
	}
	g := &testGuest{pc: pc, control: control, messages: make(chan controlMessage, 8)}
	control.OnMessage(func(message webrtc.DataChannelMessage) {
		var parsed controlMessage
		if !message.IsString || strictJSON(message.Data, &parsed, 1024) != nil {
			return
		}
		g.messages <- parsed
	})
	proposal, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(proposal); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(10 * time.Second):
		t.Fatal("guest ICE gather timeout")
	}
	sid, _ := randomID()
	plain, _ := json.Marshal(offer{SDP: pc.LocalDescription().SDP, Name: "Native phone"})
	env, err := sealSignal(key, e.Room(), envelope{Type: "signal", SID: sid, KID: kid, Kind: "offer"}, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.write(env); err != nil {
		t.Fatal(err)
	}
	var answer envelope
	select {
	case answer = <-broker.answers:
	case err := <-broker.errors:
		t.Fatal(err)
	case <-time.After(15 * time.Second):
		t.Fatal("encrypted host answer timed out")
	}
	answerPlain, err := openSignal(key, e.Room(), answer)
	if err != nil {
		t.Fatal(err)
	}
	var answerSDP struct {
		SDP string `json:"sdp"`
	}
	if err = strictJSON(answerPlain, &answerSDP, maxSignal); err != nil {
		t.Fatal(err)
	}
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP.SDP}); err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *testGuest) message(t *testing.T, kind string) controlMessage {
	t.Helper()
	select {
	case message := <-g.messages:
		if message.Type != kind {
			t.Fatalf("expected %q control message, got %+v", kind, message)
		}
		return message
	case <-time.After(10 * time.Second):
		t.Fatalf("control message %q timed out", kind)
		return controlMessage{}
	}
}

func (g *testGuest) openHTTP(t *testing.T, id string) *stream {
	t.Helper()
	request, _ := randomID()
	dc, err := g.pc.CreateDataChannel("shareme.http."+request, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newStream(dc, id, net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, nil)
	dc.OnMessage(func(message webrtc.DataChannelMessage) { s.receive(message.Data, message.IsString) })
	dc.OnError(s.abort)
	dc.OnClose(s.transportClosed)
	open := make(chan struct{})
	dc.OnOpen(func() { close(open) })
	select {
	case <-open:
	case <-time.After(10 * time.Second):
		t.Fatal("HTTP data channel open timed out")
	}
	t.Cleanup(func() { s.abort(net.ErrClosed) })
	_ = s.SetDeadline(time.Now().Add(20 * time.Second))
	return s
}

func TestPionLoopbackPairHTTPReconnectAndRevoke(t *testing.T) {
	broker := newTestBroker(t)
	e, err := newEngine(Config{
		DataDir: testDir(t), ServiceURL: broker.server.URL, LocalIP: "127.0.0.1", AllowLoopback: true,
		OnError: func(err error) { t.Logf("peer: %v", err) },
	}, newTestProtector(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err = e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var host *signalSocket
	select {
	case host = <-broker.hosts:
	case err := <-broker.errors:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("native signaling connection timed out")
	}
	_, err = e.BeginPairing()
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	invite := append([]byte(nil), e.invite...)
	e.mu.Unlock()
	guest := connectGuest(t, e, broker, host, "pair", invite)
	waitFor(t, func() bool { return len(e.PairRequests()) == 1 })
	request := e.PairRequests()[0]
	if request.Source != "127.0.0.1" || request.Name != "Native phone" {
		t.Fatalf("incorrect native pair attribution: %+v", request)
	}
	early := guest.openHTTP(t, "")
	waitFor(t, func() bool { early.mu.Lock(); defer early.mu.Unlock(); return early.wireDone })
	if err := e.DecidePair(request.ID, true); err != nil {
		t.Fatal(err)
	}
	paired := guest.message(t, "paired")
	secret, err := decodeFixed(paired.Secret, 32)
	if err != nil {
		t.Fatal(err)
	}
	if e.Devices()[0].Connected {
		t.Fatal("peer ready before credential acknowledgement")
	}
	if err = guest.control.SendText(`{"type":"paired-ack"}`); err != nil {
		t.Fatal(err)
	}
	guest.message(t, "ready")

	var openStreams []*stream
	for i := 0; i < 4; i++ {
		s := guest.openHTTP(t, paired.ID)
		openStreams = append(openStreams, s)
	}
	excess := guest.openHTTP(t, paired.ID)
	waitFor(t, func() bool { excess.mu.Lock(); defer excess.mu.Unlock(); return excess.wireDone })
	for _, s := range openStreams {
		conn, err := e.Listener().Accept()
		if err != nil {
			t.Fatal(err)
		}
		conn.(*stream).abort(net.ErrClosed)
		s.abort(net.ErrClosed)
	}

	// A returning browser authenticates using the independent durable secret.
	returning := connectGuest(t, e, broker, host, paired.ID, secret)
	returning.message(t, "ready")
	returnStream := returning.openHTTP(t, paired.ID)
	conn, err := e.Listener().Accept()
	if err != nil || conn.(interface{ PeerID() string }).PeerID() != paired.ID {
		t.Fatalf("returning device attribution: %v", err)
	}
	conn.(*stream).abort(net.ErrClosed)
	returnStream.abort(net.ErrClosed)

	content := bytes.Repeat([]byte("streamed-private-LAN-response\n"), 96*1024)
	seenPeer := make(chan string, 1)
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			seenPeer <- conn.(interface{ PeerID() string }).PeerID()
			return ctx
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != "peer.shareme" || r.RemoteAddr == "" {
				http.Error(w, "bad attribution", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.Header().Set("Connection", "close")
			_, _ = w.Write(content[:4096])
			w.(http.Flusher).Flush()
			_, _ = io.Copy(w, bytes.NewReader(content[4096:]))
		}),
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(e.Listener()) }()
	t.Cleanup(func() { _ = server.Close(); <-serveDone })
	client := guest.openHTTP(t, paired.ID)
	if _, err := client.Write([]byte("GET /api/outbox HTTP/1.1\r\nHost: peer.shareme\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if <-seenPeer != paired.ID {
		t.Fatal("HTTP connection lost device identity")
	}
	// The broker disappears while the response is blocked on consumer credit.
	broker.offline.Store(true)
	_ = host.conn.Close()
	waitFor(t, func() bool { connected, _ := e.Status(); return !connected })
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || !bytes.Equal(body, content) {
		t.Fatalf("LAN response did not survive signaling loss: %d/%d %v", len(body), len(content), err)
	}
	broker.offline.Store(false)
	select {
	case <-broker.hosts:
	case <-time.After(5 * time.Second):
		t.Fatal("native signaling did not reconnect")
	}
	waitFor(t, func() bool { connected, _ := e.Status(); return connected })
	// Revoke closes every connection, including a second authenticated peer.
	blocked := returning.openHTTP(t, paired.ID)
	if err := e.RevokeDevice(paired.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { blocked.mu.Lock(); defer blocked.mu.Unlock(); return blocked.wireDone })
	if len(e.Devices()) != 0 {
		t.Fatal("revoked credential retained")
	}
}
