package transfer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type deviceContextKey struct{}

// StartPeer serves the same approval/scanning pipeline on authenticated in-memory
// peer streams. It does not open a TCP port or enable a plaintext fallback.
func (s *Service) StartPeer(listener net.Listener, phoneURL string) error {
	if listener == nil || listener.Addr().String() != "peer.shareme" {
		return errors.New("authenticated peer listener is required")
	}
	s.lifecycle.Lock()
	s.mu.Lock()
	if s.running || s.run != nil {
		s.mu.Unlock()
		s.lifecycle.Unlock()
		return errors.New("stop receiving before changing transports")
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &serverRun{ctx: ctx, cancel: cancel, done: make(chan struct{}), host: "peer.shareme"}
	run.server = &http.Server{
		Handler:     s.handler(run),
		BaseContext: func(net.Listener) context.Context { return ctx },
		ConnContext: func(ctx context.Context, connection net.Conn) context.Context {
			peer, ok := connection.(interface{ PeerID() string })
			if !ok || !validHex(peer.PeerID(), 16) {
				return ctx
			}
			return context.WithValue(ctx, deviceContextKey{}, peer.PeerID())
		},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Minute, WriteTimeout: 30 * time.Minute,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	s.run, s.running, s.address, s.lastError = run, true, strings.TrimRight(phoneURL, "/"), ""
	s.mu.Unlock()
	s.lifecycle.Unlock()
	go func() {
		err := run.server.Serve(listener)
		close(run.done)
		run.mu.Lock()
		expected := run.closed
		run.mu.Unlock()
		if expected && (err == nil || errors.Is(err, http.ErrServerClosed)) {
			return
		}
		run.cancel()
		closeErr := run.server.Close()
		s.mu.Lock()
		if s.run == run {
			s.running, s.address = false, ""
			s.cancelPreflightsLocked()
			s.lastError = fmt.Sprintf("peer receiver stopped: %v", errors.Join(err, closeErr))
		}
		s.mu.Unlock()
		s.notify()
	}()
	s.notify()
	return nil
}

func (s *Service) serveOutbox(w http.ResponseWriter, r *http.Request, id string) {
	device, _ := r.Context().Value(deviceContextKey{}).(string)
	if device == "" || s.config.Outbox == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "transfer not found"})
		return
	}
	if id == "" {
		type offer struct {
			ID        string    `json:"id"`
			Kind      string    `json:"kind"`
			Name      string    `json:"name"`
			Size      int64     `json:"size"`
			CreatedAt time.Time `json:"createdAt"`
		}
		items := []offer{}
		for _, item := range s.config.Outbox.List(device) {
			items = append(items, offer{item.ID, item.Kind, item.Name, item.Size, item.CreatedAt})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	item, file, err := s.config.Outbox.Open(device, id)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "transfer not found"})
		return
	}
	if err != nil {
		log.Printf("open outgoing snapshot: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "transfer is unavailable; ask the PC to send it again"})
		return
	}
	defer file.Close()
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.Name}))
	w.Header().Set("Content-Type", "application/octet-stream")
	if item.Kind == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	http.ServeContent(w, r, item.Name, item.CreatedAt, file)
}
