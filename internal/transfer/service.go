// Package transfer implements receive-only transfers over HTTP on a trusted LAN.
// HTTP does not encrypt transfers. Every transfer requires an explicit local
// decision. No HTTP route exposes approval controls, paths, history, or clipboard.
package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"shareme/internal/outbox"
	"shareme/internal/shortcut"
)

const (
	defaultMaxFileBytes    = int64(2 << 30)
	maxTextBytes           = 64 << 10
	defaultApprovalTimeout = 2 * time.Minute
	maxTransfers           = 4
	maxPendingTransfers    = 8
	fileBodyOverhead       = int64(1 << 20)
)

type Config struct {
	DataDir         string        `json:"dataDir"`
	InboxDir        string        `json:"inboxDir"`
	Assets          fs.FS         `json:"-"`
	MaxFileBytes    int64         `json:"maxFileBytes"`
	OnChange        func()        `json:"-"`
	AllowLoopback   bool          `json:"allowLoopback"`
	ApprovalTimeout time.Duration `json:"approvalTimeout"`
	// OnError reports operational failures after approval, outside service
	// locks. Declines, validation failures, and normal cancellation are excluded.
	OnError func(error) `json:"-"`
	// ScanFile must honor context cancellation. It receives a closed, synced
	// quarantine file on the inbox volume; its ADS survive publication by rename.
	// A nil scanner permits text but fails closed for all file transfers.
	ScanFile      func(context.Context, string) error  `json:"-"`
	Outbox        *outbox.Store                        `json:"-"`
	ShortcutSetup func(string) (shortcut.Setup, error) `json:"-"`
}

type Status struct {
	Running      bool   `json:"running"`
	Address      string `json:"address"`
	InboxDir     string `json:"inboxDir"`
	MaxFileBytes int64  `json:"maxFileBytes"`
	Error        string `json:"error"`
}

type Item struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	MIME      string    `json:"mime"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	Path      string    `json:"path"`
}

type Service struct {
	lifecycle sync.Mutex
	mu        sync.Mutex
	config    Config
	state     diskState
	run       *serverRun
	running   bool
	address   string
	lastError string
	pending   map[string]*pendingRequest
	completed []preflightTombstone
	uploads   chan struct{}
	limiter   attemptLimiter
}

type serverRun struct {
	server *http.Server
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	host   string
	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
}

func (run *serverRun) enter() bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.closed {
		return false
	}
	run.active.Add(1)
	return true
}

func (run *serverRun) close() error {
	run.mu.Lock()
	run.closed = true
	run.mu.Unlock()
	run.cancel()
	err := run.server.Close()
	<-run.done
	run.active.Wait()
	return err
}

// New loads durable metadata. A DataDir must have one owning Service/process.
// Assets should be fs.Sub(embedFS, "frontend/dist"); only phone.html and assets/
// are reachable. AllowLoopback is exclusively for explicit development/tests.
func New(config Config) (*Service, error) {
	if strings.TrimSpace(config.DataDir) == "" {
		return nil, errors.New("data directory is required")
	}
	var err error
	config.DataDir, err = filepath.Abs(config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	if config.InboxDir == "" {
		config.InboxDir = filepath.Join(config.DataDir, "inbox")
	}
	config.InboxDir, err = filepath.Abs(config.InboxDir)
	if err != nil {
		return nil, fmt.Errorf("resolve inbox directory: %w", err)
	}
	if config.MaxFileBytes == 0 {
		config.MaxFileBytes = defaultMaxFileBytes
	}
	if config.MaxFileBytes < 1 || config.MaxFileBytes > math.MaxInt64-fileBodyOverhead {
		return nil, errors.New("maximum file size is outside the supported range")
	}
	if config.ApprovalTimeout == 0 {
		config.ApprovalTimeout = defaultApprovalTimeout
	}
	if config.ApprovalTimeout < 0 {
		return nil, errors.New("approval timeout must be positive")
	}
	for _, directory := range []string{config.DataDir, config.InboxDir} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, fmt.Errorf("create transfer directory: %w", err)
		}
	}
	service := &Service{
		config: config, uploads: make(chan struct{}, maxTransfers),
		pending: make(map[string]*pendingRequest),
	}
	if err := service.load(); err != nil {
		return nil, err
	}
	return service, nil
}

// Start binds exactly the selected local address; port zero is useful in tests.
func (s *Service) Start(ip string, port int) (result error) {
	s.lifecycle.Lock()
	changed := false
	defer func() {
		s.lifecycle.Unlock()
		if changed {
			s.notify()
		}
	}()
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("transfer service is already running")
	}
	old := s.run
	s.mu.Unlock()
	if old != nil {
		if err := old.close(); err != nil {
			return fmt.Errorf("close previous transfer server: %w", err)
		}
	}
	ip, err := validateBindIP(ip, s.config.AllowLoopback)
	if err != nil {
		return s.startFailure(err, &changed)
	}
	if port < 0 || port > 65535 {
		return s.startFailure(errors.New("port must be between 0 and 65535"), &changed)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return s.startFailure(fmt.Errorf("listen for transfers: %w", err), &changed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &serverRun{ctx: ctx, cancel: cancel, done: make(chan struct{}), host: listener.Addr().String()}
	run.server = &http.Server{
		Handler:           s.handler(run),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Minute,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	s.mu.Lock()
	s.run, s.running, s.address = run, true, "http://"+run.host
	s.lastError = ""
	s.mu.Unlock()
	changed = true
	go func() {
		err := run.server.Serve(listener)
		close(run.done)
		run.mu.Lock()
		expected := run.closed
		run.mu.Unlock()
		if err == nil || (errors.Is(err, http.ErrServerClosed) && expected) {
			return
		}
		run.cancel()
		closeErr := run.server.Close()
		s.mu.Lock()
		current := s.run == run
		if current {
			s.running, s.address = false, ""
			s.cancelPreflightsLocked()
			s.lastError = errors.Join(fmt.Errorf("serve transfers: %w", err), closeErr).Error()
		}
		s.mu.Unlock()
		if current {
			s.notify()
		}
	}()
	return nil
}

func (s *Service) startFailure(err error, changed *bool) error {
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
	*changed = true
	return err
}

// Stop cancels pending decisions, upload reads, and scans, and joins all handlers.
// A configured scanner must honor its context for shutdown to remain responsive.
func (s *Service) Stop() error {
	s.lifecycle.Lock()
	s.mu.Lock()
	run := s.run
	s.running, s.address = false, ""
	s.cancelPreflightsLocked()
	s.mu.Unlock()
	var err error
	if run != nil {
		err = run.close()
	}
	s.mu.Lock()
	s.run = nil
	if err != nil {
		s.lastError = fmt.Errorf("stop transfer server: %w", err).Error()
	}
	s.mu.Unlock()
	s.lifecycle.Unlock()
	if run != nil {
		s.notify()
	}
	return err
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Running: s.running, Address: s.address, InboxDir: s.config.InboxDir,
		MaxFileBytes: s.config.MaxFileBytes, Error: s.lastError,
	}
}

// List and Find are native-only; they are deliberately not HTTP endpoints.
func (s *Service) List() ([]Item, error) {
	s.mu.Lock()
	items := make([]Item, len(s.state.Items))
	for i, item := range s.state.Items {
		items[len(items)-1-i] = item
	}
	s.mu.Unlock()
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > 100 {
		items = items[:100]
	}
	return items, nil
}

func (s *Service) Find(id string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.state.Items {
		if item.ID == id {
			return item, nil
		}
	}
	return Item{}, fmt.Errorf("transfer item not found: %w", fs.ErrNotExist)
}

func randomID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate transfer identifier: %w", err)
	}
	return hex.EncodeToString(bytes[:]), nil
}

func (s *Service) notify() {
	if s.config.OnChange != nil {
		s.config.OnChange()
	}
}
