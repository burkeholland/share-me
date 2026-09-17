package shortcut

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

const (
	enrollmentLifetime = 5 * time.Minute
	maxEnrollments     = 64
)

type Config struct {
	DataDir         string
	LocalIP         string
	Port            int
	AllowLoopback   bool
	IsDeviceAllowed func(string) bool
	Handle          func(context.Context, Request) (Response, error)
	OnError         func(error)
	OnChange        func()
	// MaxFileBytes may lower, but never raise, the 2 GiB upload limit. Zero
	// selects 2 GiB. The transfer handler should also enforce its own limits.
	MaxFileBytes int64
}

type Status struct {
	Running     bool   `json:"running"`
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

type Setup struct {
	Version     int    `json:"version"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Name        string `json:"name"`
	Enrollment  string `json:"enrollment"`
	Fingerprint string `json:"fingerprint"`
}

type enrollment struct {
	deviceID string
	expires  time.Time
}

type limits struct {
	connections, channels, perConnection, perDevice int
	handshake, exec, idle, request, lifetime        time.Duration
}

type bucket struct {
	tokens, capacity, perSecond float64
	last                        time.Time
}

func newBucket(capacity, perSecond float64) bucket {
	return bucket{tokens: capacity, capacity: capacity, perSecond: perSecond, last: time.Now()}
}

func (b *bucket) allow(now time.Time) bool {
	b.tokens = min(b.capacity, b.tokens+max(0, now.Sub(b.last).Seconds())*b.perSecond)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type connection struct {
	raw      net.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	deviceID string
	key      string
	setup    bool
	channels int
	total    int
	events   bucket
	probes   int
}

func (c *connection) close() {
	c.cancel()
	_ = c.raw.Close()
}

type Server struct {
	cfg         Config
	ip          net.IP
	subnet      *net.IPNet
	store       identityStore
	signer      ssh.Signer
	fingerprint string
	limits      limits

	mu            sync.Mutex
	state         identity
	stateDirty    bool
	tokens        map[string]enrollment
	connections   map[*connection]struct{}
	channelCount  int
	accepts       bucket
	commands      bucket
	listener      net.Listener
	address       string
	running       bool
	closed        bool
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	done          chan struct{}
	notifications chan error
}

func New(cfg Config) (*Server, error) { return newServer(cfg, platformProtector{}) }

func newServer(cfg Config, box protector) (*Server, error) {
	if strings.TrimSpace(cfg.DataDir) == "" || cfg.IsDeviceAllowed == nil || cfg.Handle == nil ||
		cfg.Port < 0 || cfg.Port > 65535 || cfg.MaxFileBytes < 0 || cfg.MaxFileBytes > maxFileBytes || box == nil {
		return nil, errors.New("invalid shortcut configuration")
	}
	if cfg.MaxFileBytes == 0 {
		cfg.MaxFileBytes = maxFileBytes
	}
	ip, subnet, err := selectedNetwork(cfg.LocalIP, cfg.AllowLoopback)
	if err != nil {
		return nil, err
	}
	store := identityStore{path: filepath.Join(cfg.DataDir, stateFile), box: box}
	state, signer, err := store.load()
	if err != nil {
		return nil, err
	}
	state, err = store.prune(state, cfg.IsDeviceAllowed)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg: cfg, ip: ip, subnet: subnet, store: store, signer: signer, state: state,
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		address:     net.JoinHostPort(ip.String(), strconv.Itoa(cfg.Port)),
		tokens:      map[string]enrollment{}, connections: map[*connection]struct{}{},
		accepts: newBucket(32, 1), commands: newBucket(64, 2),
		done: make(chan struct{}), notifications: make(chan error, 16),
		limits: limits{
			connections: 16, channels: 16, perConnection: 2, perDevice: 4,
			handshake: 10 * time.Second, exec: 10 * time.Second,
			idle: 90 * time.Second, request: 30 * time.Minute, lifetime: 35 * time.Minute,
		},
	}
	if cfg.OnChange != nil || cfg.OnError != nil {
		go s.deliverNotifications()
	}
	return s, nil
}

func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Running: s.running, Address: s.address, Fingerprint: s.fingerprint}
}

func (s *Server) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shortcut requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed || s.running {
		s.mu.Unlock()
		return errors.New("shortcut server is already started or closed")
	}
	listener, err := net.Listen("tcp4", s.address)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.listener, s.address, s.running = listener, listener.Addr().String(), true
	s.wg.Add(2)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		<-s.ctx.Done()
		s.stop()
	}()
	go s.acceptLoop(listener)
	s.notify(nil)
	return nil
}

func (s *Server) stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed, s.running = true, false
	clear(s.tokens)
	listener, cancel := s.listener, s.cancel
	connections := make([]*connection, 0, len(s.connections))
	for c := range s.connections {
		connections = append(connections, c)
	}
	close(s.done)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if listener != nil {
		_ = listener.Close()
	}
	for _, c := range connections {
		c.close()
	}
}

func (s *Server) Close() error {
	s.stop()
	s.wg.Wait()
	return nil
}

func checkDeviceAllowed(callback func(string) bool, deviceID string) (allowed bool, err error) {
	defer func() {
		if recover() != nil {
			allowed = false
			err = errors.New("shortcut device authorization callback panicked")
		}
	}()
	return callback(deviceID), nil
}

func (s *Server) allowed(deviceID string) bool {
	allowed, err := checkDeviceAllowed(s.cfg.IsDeviceAllowed, deviceID)
	if err != nil {
		s.notify(err)
	}
	return allowed
}

func (s *Server) notify(err error) {
	select {
	case s.notifications <- err:
	default:
	}
}

func (s *Server) deliverNotifications() {
	invoke := func(err error) {
		defer func() { _ = recover() }()
		if err == nil && s.cfg.OnChange != nil {
			s.cfg.OnChange()
		} else if err != nil && s.cfg.OnError != nil {
			s.cfg.OnError(err)
		}
	}
	for {
		select {
		case <-s.done:
			invoke(nil)
			return
		case err := <-s.notifications:
			invoke(err)
		}
	}
}

func enrollmentID(token string) string {
	hash := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// CreateSetup authorizes enrollment for the paired browser deviceID. The name
// is the Windows PC's display name, not an authorization identity.
func (s *Server) CreateSetup(deviceID, name string) (Setup, error) {
	if !validID(deviceID) || !validName(name, 80) || !s.allowed(deviceID) {
		return Setup{}, errors.New("shortcut setup requires an allowed paired browser device")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Setup{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.closed {
		return Setup{}, errors.New("shortcut receiver is not running")
	}
	now := time.Now()
	for id, value := range s.tokens {
		if !now.Before(value.expires) {
			delete(s.tokens, id)
		}
	}
	if len(s.tokens) >= maxEnrollments {
		return Setup{}, errors.New("too many pending shortcut enrollments")
	}
	port := s.listener.Addr().(*net.TCPAddr).Port
	s.tokens[enrollmentID(token)] = enrollment{deviceID: deviceID, expires: now.Add(enrollmentLifetime)}
	return Setup{Version: 1, Host: s.ip.String(), Port: port, Name: name, Enrollment: token, Fingerprint: s.fingerprint}, nil
}

func (s *Server) RevokeDevice(deviceID string) error {
	if !validID(deviceID) {
		return errors.New("invalid shortcut device ID")
	}
	s.mu.Lock()
	next := s.state.copy()
	changed := false
	for key, device := range next.Keys {
		if device == deviceID {
			delete(next.Keys, key)
			changed = true
		}
	}
	for token, enrollment := range s.tokens {
		if enrollment.deviceID == deviceID {
			delete(s.tokens, token)
		}
	}
	connections := make([]*connection, 0)
	for c := range s.connections {
		if c.deviceID == deviceID {
			connections = append(connections, c)
		}
	}
	// Memory revocation is unconditional, even when disk protection/replacement
	// fails. The parent's authorization callback also gates a subsequent load.
	s.state = next
	s.stateDirty = s.stateDirty || changed
	s.mu.Unlock()
	for _, c := range connections {
		c.close()
	}
	var err error
	s.mu.Lock()
	if s.stateDirty {
		// Save the current snapshot, not the pre-unlock copy: another device may
		// have enrolled or been revoked while connections were being closed.
		err = s.store.save(s.state)
		if err == nil {
			s.stateDirty = false
		}
	}
	s.mu.Unlock()
	s.notify(nil)
	return err
}

func (s *Server) acceptLoop(listener net.Listener) {
	defer s.wg.Done()
	for {
		raw, err := listener.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				s.notify(errors.New("shortcut listener failed"))
				s.stop()
			}
			return
		}
		s.mu.Lock()
		if s.closed || !s.permittedRemote(raw.RemoteAddr()) || len(s.connections) >= s.limits.connections || !s.accepts.allow(time.Now()) {
			s.mu.Unlock()
			_ = raw.Close()
			continue
		}
		ctx, cancel := context.WithCancel(s.ctx)
		c := &connection{raw: raw, ctx: ctx, cancel: cancel, events: newBucket(32, 1)}
		s.connections[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serveConnection(c)
	}
}

func (s *Server) candidateLocked(username, key string) (device, token string, ok bool) {
	if s.closed {
		return "", "", false
	}
	if username == "shareme" {
		device = s.state.Keys[key]
		return device, "", device != ""
	}
	if !strings.HasPrefix(username, "setup-") || !validToken(strings.TrimPrefix(username, "setup-")) {
		return "", "", false
	}
	token = enrollmentID(strings.TrimPrefix(username, "setup-"))
	enrollment, exists := s.tokens[token]
	if !exists || !time.Now().Before(enrollment.expires) {
		return "", "", false
	}
	if existing := s.state.Keys[key]; existing != "" && existing != enrollment.deviceID {
		return "", "", false
	}
	return enrollment.deviceID, token, true
}

func (s *Server) authorize(c *connection, metadata ssh.ConnMetadata, public ssh.PublicKey) (*ssh.Permissions, error) {
	denied := errors.New("shortcut public key authentication denied")
	key, err := publicKeyID(public)
	if err != nil {
		return nil, denied
	}
	s.mu.Lock()
	c.probes++
	if c.probes > 8 {
		s.mu.Unlock()
		return nil, denied
	}
	device, token, ok := s.candidateLocked(metadata.User(), key)
	s.mu.Unlock()
	if !ok || !s.allowed(device) {
		return nil, denied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, currentToken, ok := s.candidateLocked(metadata.User(), key)
	if !ok || current != device || currentToken != token || c.ctx.Err() != nil {
		return nil, denied
	}
	c.deviceID = device
	return &ssh.Permissions{Extensions: map[string]string{"device": device, "key": key, "enrollment": token}}, nil
}

// This is called only after NewServerConn has verified the client signature.
// PublicKeyCallback can be invoked for unsigned probes, so it never saves keys
// or consumes enrollment tokens.
func (s *Server) claim(c *connection, conn *ssh.ServerConn) error {
	if conn.Permissions == nil {
		return errors.New("missing shortcut authentication")
	}
	permissions := conn.Permissions.Extensions
	device, key, token := permissions["device"], permissions["key"], permissions["enrollment"]
	if !s.allowed(device) {
		return errors.New("shortcut device was revoked")
	}
	s.mu.Lock()
	if s.closed || c.ctx.Err() != nil {
		s.mu.Unlock()
		return errors.New("shortcut receiver closed")
	}
	count := 0
	for other := range s.connections {
		if other != c && other.deviceID == device && other.key != "" {
			count++
		}
	}
	current, currentToken, ok := s.candidateLocked(conn.User(), key)
	if !ok || current != device || currentToken != token || count >= s.limits.perDevice {
		s.mu.Unlock()
		return errors.New("shortcut authentication no longer valid")
	}
	if token != "" {
		delete(s.tokens, token)
		if s.state.Keys[key] == "" && len(s.state.Keys) >= maxKeys {
			s.mu.Unlock()
			return errors.New("shortcut enrolled key limit reached")
		}
		next := s.state.copy()
		next.Keys[key] = device
		if err := s.store.save(next); err != nil {
			s.mu.Unlock()
			s.notify(errors.New("could not persist shortcut enrollment"))
			return errors.New("could not persist shortcut enrollment")
		}
		s.state = next
		s.stateDirty = false
	}
	c.deviceID, c.key, c.setup = device, key, token != ""
	s.mu.Unlock()
	if token != "" {
		s.notify(nil)
	}
	return nil
}

func (s *Server) serveConnection(c *connection) {
	var workers sync.WaitGroup
	defer func() {
		c.close()
		workers.Wait()
		s.mu.Lock()
		delete(s.connections, c)
		s.mu.Unlock()
		s.wg.Done()
	}()
	algorithms := ssh.SupportedAlgorithms()
	cfg := &ssh.ServerConfig{
		Config:                  ssh.Config{KeyExchanges: algorithms.KeyExchanges, Ciphers: algorithms.Ciphers, MACs: algorithms.MACs},
		PublicKeyAuthAlgorithms: algorithms.PublicKeyAuths,
		MaxAuthTries:            3,
		PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return s.authorize(c, metadata, key)
		},
	}
	cfg.AddHostKey(s.signer)
	_ = c.raw.SetDeadline(time.Now().Add(s.limits.handshake))
	conn, channels, requests, err := ssh.NewServerConn(c.raw, cfg)
	if err != nil {
		// SSH errors can contain the setup username (a bearer token). Never
		// forward handshake errors or connection metadata to logging callbacks.
		return
	}
	defer conn.Close()
	if err = s.claim(c, conn); err != nil {
		return
	}
	_ = c.raw.SetDeadline(time.Now().Add(s.limits.lifetime))
	idle := time.NewTimer(s.limits.idle)
	defer idle.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-idle.C:
			s.mu.Lock()
			active := c.channels
			s.mu.Unlock()
			if active == 0 {
				return
			}
			idle.Reset(s.limits.idle)
		case request, ok := <-requests:
			if !ok || !c.events.allow(time.Now()) {
				return
			}
			_ = request.Reply(false, nil)
		case channel, ok := <-channels:
			if !ok || !c.events.allow(time.Now()) {
				return
			}
			if channel.ChannelType() != "session" || len(channel.ExtraData()) != 0 {
				_ = channel.Reject(ssh.Prohibited, "only plain session channels are permitted")
				continue
			}
			s.mu.Lock()
			permitted := !s.closed && c.ctx.Err() == nil && s.channelCount < s.limits.channels &&
				c.channels < s.limits.perConnection && c.total < 64 && s.commands.allow(time.Now())
			if permitted {
				s.channelCount++
				c.channels++
				c.total++
			}
			s.mu.Unlock()
			if !permitted {
				_ = channel.Reject(ssh.ResourceShortage, "shortcut channel limit reached")
				continue
			}
			stream, streamRequests, err := channel.Accept()
			if err != nil {
				s.releaseChannel(c)
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer s.releaseChannel(c)
				s.serveChannel(c, stream, streamRequests)
			}()
		}
	}
}

func (s *Server) releaseChannel(c *connection) {
	s.mu.Lock()
	s.channelCount--
	c.channels--
	s.mu.Unlock()
}

func (s *Server) commandAllowed(c *connection) bool {
	if !s.allowed(c.deviceID) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && c.ctx.Err() == nil && s.state.Keys[c.key] == c.deviceID
}

type execution struct {
	response Response
	err      error
}

func finishChannel(channel ssh.Channel, result execution) {
	exit := uint32(0)
	if result.err != nil {
		exit = 1
		if _, err := io.WriteString(channel.Stderr(), result.err.Error()+"\n"); err != nil {
			return
		}
	} else {
		if _, err := channel.Write(result.response.Body); err != nil {
			return
		}
		if _, err := io.WriteString(channel, "\n"); err != nil {
			return
		}
	}
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exit}))
}

func (s *Server) serveChannel(c *connection, channel ssh.Channel, requests <-chan *ssh.Request) {
	ctx, cancel := context.WithTimeout(c.ctx, s.limits.request)
	stop := context.AfterFunc(ctx, c.close)
	defer func() {
		stop()
		cancel()
		_ = channel.Close()
	}()
	timer := time.NewTimer(s.limits.exec)
	defer timer.Stop()
	var request Request
	rejected := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			finishChannel(channel, execution{err: errors.New("timed out waiting for exec")})
			return
		case incoming, ok := <-requests:
			if !ok {
				return
			}
			if incoming.Type != "exec" {
				_ = incoming.Reply(false, nil)
				rejected++
				if rejected >= 8 {
					return
				}
				continue
			}
			var payload struct{ Command string }
			err := ssh.Unmarshal(incoming.Payload, &payload)
			if err == nil {
				request, err = parseCommand(payload.Command, c.setup)
			}
			if err == nil && !s.commandAllowed(c) {
				err = errors.New("shortcut device is no longer allowed")
			}
			if err != nil {
				// Acknowledge exec so native clients can receive useful stderr
				// and a nonzero exit code rather than an opaque start failure.
				_ = incoming.Reply(true, nil)
				finishChannel(channel, execution{err: errors.New("invalid or unauthorized shareme-v1 command")})
				return
			}
			_ = incoming.Reply(true, nil)
			goto execute
		}
	}

execute:
	request.DeviceID, request.RemoteAddr = c.deviceID, c.raw.RemoteAddr().String()
	result := make(chan execution, 1)
	go func() {
		response, err := s.execute(ctx, channel, request)
		result <- execution{response: response, err: err}
	}()
	for {
		select {
		case finished := <-result:
			finishChannel(channel, finished)
			return
		case <-ctx.Done():
			c.close()
			<-result
			return
		case incoming, ok := <-requests:
			if !ok {
				cancel()
				c.close()
				<-result
				return
			}
			_ = incoming.Reply(false, nil)
			rejected++
			if rejected >= 8 {
				cancel()
			}
		}
	}
}

func (s *Server) execute(ctx context.Context, channel ssh.Channel, request Request) (Response, error) {
	var body *streamBody
	if request.Operation == "upload" {
		body = &streamBody{ctx: ctx, reader: channel, remaining: s.cfg.MaxFileBytes}
		request.Body = body
	} else {
		limit := int64(0)
		switch request.Operation {
		case "request":
			limit = maxPreflight
		case "text":
			limit = maxText
		}
		data, err := readBounded(channel, limit)
		if err != nil {
			return Response{}, errors.New("invalid, oversized, or interrupted stdin")
		}
		if !utf8.Valid(data) || request.Operation == "request" && !json.Valid(data) {
			return Response{}, errors.New("stdin must be UTF-8; request stdin must be JSON")
		}
		request.Body = bytes.NewReader(data)
	}
	if request.Operation == "setup" {
		return Response{StatusCode: 200, Body: []byte(`{"status":"ready"}`)}, nil
	}
	response, err := s.handle(ctx, request)
	if err != nil {
		s.notify(errors.New("shortcut request handler failed"))
		return Response{}, errors.New("shortcut request could not be completed")
	}
	if ctx.Err() != nil {
		return Response{}, errors.New("shortcut request was canceled")
	}
	if !validResponse(response) {
		s.notify(errors.New("shortcut handler returned an invalid JSON response"))
		return Response{}, errors.New("shortcut handler returned an invalid response")
	}
	if response.StatusCode < 300 && body != nil {
		if err = body.finish(); err != nil {
			return Response{}, fmt.Errorf("incomplete upload: %w", err)
		}
	}
	return response, nil
}

func (s *Server) handle(ctx context.Context, request Request) (response Response, err error) {
	defer func() {
		if recover() != nil {
			response, err = Response{}, errors.New("shortcut handler panicked")
		}
	}()
	return s.cfg.Handle(ctx, request)
}
