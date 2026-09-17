package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func testEngine(t *testing.T) (*Engine, *testProtector) {
	t.Helper()
	box := newTestProtector(t)
	e, err := newEngine(Config{DataDir: testDir(t), ServiceURL: "http://127.0.0.1:1", LocalIP: "127.0.0.1", AllowLoopback: true}, box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e, box
}

func testPairRequest(t *testing.T, e *Engine) (*remotePeer, *testWire) {
	t.Helper()
	id, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	w := &testWire{}
	e.mu.Lock()
	defer e.mu.Unlock()
	p := &remotePeer{
		engine: e, sid: id, kid: "pair", name: "Phone", inviteGen: e.inviteGen,
		control: w, streams: map[string]*stream{},
		done: make(chan struct{}), opened: make(chan struct{}),
		approved: make(chan struct{}), authorized: make(chan struct{}),
		request: &PairRequest{ID: id, Name: "Phone", Source: "127.0.0.1", CreatedAt: time.Now().UTC()},
	}
	e.peers[id] = p
	return p, w
}

func TestPairingPersistenceAndRevocation(t *testing.T) {
	e, box := testEngine(t)
	// Callbacks may synchronously query the Engine without deadlocking.
	e.cfg.OnChange = func() { e.Devices(); e.PairRequests(); e.Status() }
	pairURL, err := e.BeginPairing()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(pairURL)
	fragment, _ := url.ParseQuery(u.Fragment)
	invite, err := decodeFixed(fragment.Get("pair"), 32)
	if err != nil || fragment.Get("room") != e.Room() || strings.Contains(e.PhoneURL(), "pair=") {
		t.Fatal("incorrect private invitation URL")
	}
	p, wire := testPairRequest(t, e)
	box.fail = true
	if err := e.DecidePair(p.sid, true); err == nil || len(e.Devices()) != 0 || len(e.PairRequests()) != 1 {
		t.Fatal("pair accepted without durable credential")
	}
	box.fail = false
	if err := e.DecidePair(p.sid, true); err != nil {
		t.Fatal(err)
	}
	if len(e.PairRequests()) != 0 || len(e.invite) != 0 || e.Devices()[0].Connected {
		t.Fatal("pair authorized before acknowledgement or invitation not consumed")
	}
	wire.mu.Lock()
	message := wire.texts[0]
	wire.mu.Unlock()
	var paired controlMessage
	if err := json.Unmarshal([]byte(message), &paired); err != nil {
		t.Fatal(err)
	}
	secret, err := decodeFixed(paired.Secret, 32)
	if err != nil || bytes.Equal(secret, invite) || paired.Type != "paired" || !validID(paired.ID) {
		t.Fatal("device credential was not independently generated")
	}
	saved, _, err := e.store.load()
	if err != nil || !bytes.Equal(saved.Devices[paired.ID].Secret, secret) {
		t.Fatal("credential was not persisted before delivery")
	}
	p.controlReceived(webrtc.DataChannelMessage{IsString: true, Data: []byte(`{"type":"paired-ack"}`)})
	if !e.Devices()[0].Connected {
		t.Fatal("acknowledged device not authorized")
	}
	if err := e.RenameDevice(paired.ID, "My iPhone"); err != nil {
		t.Fatal(err)
	}
	if err := e.RenameDevice(paired.ID, "unsafe\nname"); err == nil {
		t.Fatal("invalid name accepted")
	}
	w := &testWire{}
	s := newStream(w, paired.ID, net.TCPAddr{}, nil)
	e.mu.Lock()
	p.streams["request"] = s
	e.mu.Unlock()
	box.fail = true
	if err := e.RevokeDevice(paired.ID); err == nil || !e.Devices()[0].Connected || w.closed.Load() {
		t.Fatal("revocation closed streams before persistence succeeded")
	}
	box.fail = false
	if err := e.RevokeDevice(paired.ID); err != nil {
		t.Fatal(err)
	}
	saved, _, err = e.store.load()
	if err != nil || len(saved.Devices) != 0 || len(e.Devices()) != 0 || !w.closed.Load() {
		t.Fatal("revocation did not persist and close streams")
	}
}

func TestInvitationCancellationReplacementAndDecline(t *testing.T) {
	e, _ := testEngine(t)
	first, _ := e.BeginPairing()
	p, _ := testPairRequest(t, e)
	second, err := e.BeginPairing()
	if err != nil || first == second || len(e.PairRequests()) != 0 || !p.closed {
		t.Fatal("replacement invitation retained old requests")
	}
	p, wire := testPairRequest(t, e)
	if err := e.DecidePair(p.sid, false); err != nil {
		t.Fatal(err)
	}
	if !p.closed || len(e.Devices()) != 0 || len(e.invite) != 32 {
		t.Fatal("decline changed persistent credentials")
	}
	wire.mu.Lock()
	response := wire.texts[0]
	wire.mu.Unlock()
	if !strings.Contains(response, "Connection declined on PC") {
		t.Fatal("native decline not communicated")
	}
	p, _ = testPairRequest(t, e)
	e.CancelPairing()
	if !p.closed || len(e.PairRequests()) != 0 || len(e.invite) != 0 {
		t.Fatal("cancellation retained invitation")
	}
	if err := e.DecidePair(p.sid, true); err == nil {
		t.Fatal("cancelled pairing accepted")
	}
}

func TestNetworkAndConfigurationPolicy(t *testing.T) {
	e, _ := testEngine(t)
	for _, address := range []string{"8.8.8.8", "192.168.1.2", "::1", "0.0.0.0"} {
		if e.permittedRemote(net.ParseIP(address)) {
			t.Fatalf("loopback engine accepted %s", address)
		}

	}
	lan := &Engine{ip: net.IPv4(192, 168, 1, 10), subnet: &net.IPNet{IP: net.IPv4(192, 168, 1, 0), Mask: net.CIDRMask(24, 32)}}
	for _, address := range []string{"192.168.2.1", "10.0.0.2", "192.168.1.255", "192.168.1.0", "127.0.0.1", "1.1.1.1"} {
		if lan.permittedRemote(net.ParseIP(address)) {
			t.Fatalf("LAN engine accepted %s", address)
		}
	}
	if !lan.permittedRemote(net.ParseIP("192.168.1.20")) {
		t.Fatal("LAN engine rejected subnet host")
	}
	for _, cfg := range []Config{
		{DataDir: ".", ServiceURL: "http://example.com", LocalIP: "127.0.0.1", AllowLoopback: true},
		{DataDir: ".", ServiceURL: "https://example.com", LocalIP: "127.0.0.1"},
		{DataDir: ".", ServiceURL: "https://u:p@example.com", LocalIP: "127.0.0.1", AllowLoopback: true},
		{DataDir: ".", ServiceURL: "https://example.com/path", LocalIP: "127.0.0.1", AllowLoopback: true},
	} {
		if _, err := newEngine(cfg, newTestProtector(t)); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
	badSDP := "v=0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\na=candidate:1 1 UDP 123 8.8.8.8 1234 typ host\r\n"
	if _, err := e.filterOffer(context.Background(), badSDP); err == nil {
		t.Fatal("public candidate accepted")
	}
}

func TestOfferAuthorizationAndDeviceLimits(t *testing.T) {
	e, _ := testEngine(t)
	e.mu.Lock()
	e.started = true
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.mu.Unlock()
	sid := strings.Repeat("a", 32)
	env := envelope{Type: "signal", SID: sid, KID: strings.Repeat("b", 32), Kind: "offer"}
	if err := e.handleOffer(env, func(any) error { t.Fatal("unauthorized answer"); return nil }); err == nil {
		t.Fatal("unknown/revoked device accepted")
	}
	_, _ = e.BeginPairing()
	env.KID = "pair"
	e.mu.Lock()
	e.seen[sid] = struct{}{}
	e.mu.Unlock()
	if err := e.handleOffer(env, func(any) error { return nil }); err == nil {
		t.Fatal("replayed session accepted")
	}
	e.mu.Lock()
	delete(e.seen, sid)
	e.mu.Unlock()
	for i := 0; i < 8; i++ {
		testPairRequest(t, e)
	}
	if err := e.handleOffer(env, func(any) error { return nil }); err == nil {
		t.Fatal("ninth peer accepted")
	}
	e.CancelPairing()
	e.mu.Lock()
	for i := 0; i < 16; i++ {
		id, _ := randomID()
		e.state.Devices[id] = credential{ID: id, Name: "Phone", Secret: make([]byte, 32), CreatedAt: time.Now()}
	}
	e.mu.Unlock()
	if _, err := e.BeginPairing(); err == nil {
		t.Fatal("seventeenth device permitted")
	}
}
