package peer

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

type Config struct {
	DataDir, ServiceURL, LocalIP string
	OnChange                     func()
	OnError                      func(error)
	AllowLoopback                bool
}

type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Connected bool      `json:"connected"`
	CreatedAt time.Time `json:"createdAt"`
}

type PairRequest struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"createdAt"`
}

type Engine struct {
	cfg        Config
	origin     string
	ip         net.IP
	subnet     *net.IPNet
	iface      *net.Interface
	key        *ecdsa.PrivateKey
	room       string
	store      identityStore
	listener   *listener
	persistMu  sync.Mutex
	mu         sync.Mutex
	state      identity
	invite     []byte
	inviteGen  uint64
	peers      map[string]*remotePeer
	seen       map[string]struct{}
	signalRate rateLimit
	ctx        context.Context
	cancel     context.CancelFunc
	started    bool
	closed     bool
	connected  bool
	message    string
	socket     *websocket.Conn
	wg         sync.WaitGroup
	stopOnce   sync.Once
}

// New opens only DPAPI-protected identity state. AllowLoopback relaxes network
// policy for tests, never identity protection or certificate verification.
func New(cfg Config) (*Engine, error) {
	return newEngine(cfg, platformProtector{})
}

func newEngine(cfg Config, box protector) (*Engine, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("peer data directory is required")
	}
	u, err := url.Parse(cfg.ServiceURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return nil, errors.New("service URL must be an HTTPS origin")
	}
	testHTTP := cfg.AllowLoopback && u.Scheme == "http" &&
		(u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())
	if u.Scheme != "https" && !testHTTP {
		return nil, errors.New("service URL must use HTTPS")
	}
	ip, subnet, iface, err := selectedNetwork(cfg.LocalIP, cfg.AllowLoopback)
	if err != nil {
		return nil, err
	}
	store := identityStore{path: filepath.Join(cfg.DataDir, "peer-identity.dpapi"), box: box}
	state, key, err := store.load()
	if err != nil {
		return nil, err
	}
	return &Engine{
		cfg: cfg, origin: strings.TrimRight(u.String(), "/"),
		ip: ip, subnet: subnet, iface: iface, key: key, room: roomFor(key),
		store: store, state: state, listener: newListener(), peers: map[string]*remotePeer{},
		seen: map[string]struct{}{}, message: "Not started",
	}, nil
}

func selectedNetwork(local string, allowLoopback bool) (net.IP, *net.IPNet, *net.Interface, error) {
	ip := net.ParseIP(local).To4()
	if ip == nil || !(ip.IsPrivate() || allowLoopback && ip.IsLoopback()) {
		return nil, nil, nil, errors.New("select a private IPv4 interface")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, nil, err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			n, ok := addr.(*net.IPNet)
			if ok && n.IP.Equal(ip) {
				return ip, &net.IPNet{IP: ip.Mask(n.Mask), Mask: n.Mask}, &iface, nil
			}
		}
	}
	return nil, nil, nil, errors.New("selected IPv4 address is not on an active interface")
}

func (e *Engine) permittedRemote(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || !e.subnet.Contains(ip) {
		return false
	}
	if ip.IsLoopback() {
		return e.cfg.AllowLoopback && e.ip.IsLoopback()
	}
	if !ip.IsPrivate() || e.ip.IsLoopback() {
		return false
	}
	// Do not send ICE checks to subnet network/broadcast addresses.
	if ip.Equal(e.subnet.IP) {
		return false
	}
	broadcast := make(net.IP, 4)
	for i := range broadcast {
		broadcast[i] = e.subnet.IP.To4()[i] | ^e.subnet.Mask[i]
	}
	return !ip.Equal(broadcast)
}

func (e *Engine) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return net.ErrClosed
	}
	if e.started {
		e.mu.Unlock()
		return nil
	}
	e.started = true
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.message = "Connecting to signaling service"
	e.wg.Add(2)
	go func() {
		defer e.wg.Done()
		e.signalLoop()
	}()
	go func() {
		defer e.wg.Done()
		<-e.ctx.Done()
		e.stop()
	}()
	e.mu.Unlock()
	e.changed()
	return nil
}

func (e *Engine) changed() {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if !closed && e.cfg.OnChange != nil {
		e.cfg.OnChange()
	}
}

func (e *Engine) report(err error) {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if !closed && err != nil && e.cfg.OnError != nil {
		e.cfg.OnError(err)
	}
}

func (e *Engine) setStatus(connected bool, message string) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.connected, e.message = connected, message
	e.mu.Unlock()
	e.changed()
}

func (e *Engine) stop() {
	e.stopOnce.Do(func() {
		e.persistMu.Lock()
		e.mu.Lock()
		e.closed = true
		e.connected, e.message = false, "Stopped"
		clear(e.invite)
		e.invite = nil
		if e.cancel != nil {
			e.cancel()
		}
		socket := e.socket
		peers := make([]*remotePeer, 0, len(e.peers))
		for _, p := range e.peers {
			peers = append(peers, p)
		}
		e.mu.Unlock()
		e.persistMu.Unlock()
		_ = e.listener.Close()
		if socket != nil {
			_ = socket.Close()
		}
		for _, p := range peers {
			p.shutdown()
		}
	})
}

func (e *Engine) Close() error {
	e.stop()
	e.wg.Wait()
	return nil
}

func (e *Engine) Listener() net.Listener { return e.listener }
func (e *Engine) Room() string           { return e.room }
func (e *Engine) PhoneURL() string       { return e.origin + "/#room=" + e.room }

func (e *Engine) BeginPairing() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	e.persistMu.Lock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		e.persistMu.Unlock()
		return "", net.ErrClosed
	}
	if len(e.state.Devices) >= 16 {
		e.mu.Unlock()
		e.persistMu.Unlock()
		return "", errors.New("device limit reached")
	}
	clear(e.invite)
	e.invite = key
	e.inviteGen++
	peers := e.pendingPeersLocked(nil)
	url := e.PhoneURL() + "&pair=" + raw64.EncodeToString(key)
	e.mu.Unlock()
	e.persistMu.Unlock()
	for _, p := range peers {
		p.shutdown()
	}
	e.changed()
	return url, nil
}

func (e *Engine) CancelPairing() {
	e.persistMu.Lock()
	e.mu.Lock()
	clear(e.invite)
	e.invite = nil
	e.inviteGen++
	peers := e.pendingPeersLocked(nil)
	e.mu.Unlock()
	e.persistMu.Unlock()
	for _, p := range peers {
		p.shutdown()
	}
	e.changed()
}

func (e *Engine) pendingPeersLocked(except *remotePeer) []*remotePeer {
	var peers []*remotePeer
	for _, p := range e.peers {
		if p != except && p.kid == "pair" && p.deviceID == "" {
			peers = append(peers, p)
		}
	}
	return peers
}

func (e *Engine) PairRequests() []PairRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := []PairRequest{}
	for _, p := range e.peers {
		if p.request != nil {
			result = append(result, *p.request)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (e *Engine) DecidePair(id string, accept bool) error {
	e.persistMu.Lock()
	e.mu.Lock()
	p := e.peers[id]
	if e.closed || p == nil || p.request == nil || p.closed || p.inviteGen != e.inviteGen || len(e.invite) != 32 {
		e.mu.Unlock()
		e.persistMu.Unlock()
		return errors.New("pairing request is no longer available")
	}
	if !accept {
		p.request = nil
		e.mu.Unlock()
		e.persistMu.Unlock()
		_ = p.sendControl(controlMessage{Type: "error", Message: "Connection declined on PC"})
		p.shutdown()
		e.changed()
		return nil
	}
	if len(e.state.Devices) >= 16 {
		e.mu.Unlock()
		e.persistMu.Unlock()
		return errors.New("device limit reached")
	}
	next := copyIdentity(e.state)
	name := p.request.Name
	e.mu.Unlock()
	deviceID, err := randomID()
	secret := make([]byte, 32)
	if err == nil {
		_, err = rand.Read(secret)
	}
	if err == nil {
		next.Devices[deviceID] = credential{ID: deviceID, Name: name, Secret: secret, CreatedAt: time.Now().UTC()}
		err = e.store.save(next)
	}
	if err != nil {
		e.persistMu.Unlock()
		return err
	}
	// An eager acknowledgement cannot overtake delivery of the credential.
	p.controlSend.Lock()
	e.mu.Lock()
	e.state = next
	clear(e.invite)
	e.invite = nil
	e.inviteGen++
	p.deviceID, p.awaitingAck, p.request = deviceID, true, nil
	close(p.approved)
	others := e.pendingPeersLocked(p)
	e.mu.Unlock()
	e.persistMu.Unlock()
	err = p.sendControlLocked(controlMessage{Type: "paired", ID: deviceID, Name: name, Secret: raw64.EncodeToString(secret)})
	p.controlSend.Unlock()
	for _, other := range others {
		other.shutdown()
	}
	if err != nil {
		p.shutdown()
	}
	e.changed()
	return err
}

func (e *Engine) Devices() []Device {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]Device, 0, len(e.state.Devices))
	for _, c := range e.state.Devices {
		d := Device{ID: c.ID, Name: c.Name, CreatedAt: c.CreatedAt}
		for _, p := range e.peers {
			if p.deviceID == c.ID && p.ready && !p.closed {
				d.Connected = true
				break
			}
		}
		result = append(result, d)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (e *Engine) RenameDevice(id, name string) error {
	if !validName(name) {
		return errors.New("device name must be 1–80 UTF-8 bytes without control characters")
	}
	e.persistMu.Lock()
	e.mu.Lock()
	c, ok := e.state.Devices[id]
	if !ok || e.closed {
		e.mu.Unlock()
		e.persistMu.Unlock()
		return errors.New("unknown device")
	}
	next := copyIdentity(e.state)
	c.Name = name
	next.Devices[id] = c
	e.mu.Unlock()
	err := e.store.save(next)
	if err == nil {
		e.mu.Lock()
		e.state = next
		e.mu.Unlock()
	}
	e.persistMu.Unlock()
	if err == nil {
		e.changed()
	}
	return err
}

func (e *Engine) RevokeDevice(id string) error {
	e.persistMu.Lock()
	e.mu.Lock()
	if _, ok := e.state.Devices[id]; !ok || e.closed {
		e.mu.Unlock()
		e.persistMu.Unlock()
		return errors.New("unknown device")
	}
	next := copyIdentity(e.state)
	delete(next.Devices, id)
	e.mu.Unlock()
	if err := e.store.save(next); err != nil {
		e.persistMu.Unlock()
		return err
	}
	e.mu.Lock()
	e.state = next
	var peers []*remotePeer
	for _, p := range e.peers {
		if p.deviceID == id || p.kid == id {
			// Publish loss of authorization atomically with the durable state.
			p.ready = false
			peers = append(peers, p)
		}
	}
	e.mu.Unlock()
	e.persistMu.Unlock()
	for _, p := range peers {
		p.shutdown()
	}
	e.changed()
	return nil
}

func (e *Engine) Status() (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.connected, e.message
}

func (e *Engine) newPeerConnection() (*webrtc.PeerConnection, error) {
	var settings webrtc.SettingEngine
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settings.SetInterfaceFilter(func(name string) bool { return name == e.iface.Name })
	settings.SetIPFilter(func(ip net.IP) bool { return e.ip.Equal(ip) })
	settings.SetRemoteIPFilter(e.permittedRemote)
	settings.SetIncludeLoopbackCandidate(e.cfg.AllowLoopback)
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	settings.SetSCTPMaxMessageSize(frameLimit)
	settings.SetSCTPMaxReceiveBufferSize(windowSize * 4)
	settings.SetICETimeouts(10*time.Second, 20*time.Second, 2*time.Second)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settings))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{}})
	if err != nil {
		return nil, fmt.Errorf("create LAN peer: %w", err)
	}
	return pc, nil
}
