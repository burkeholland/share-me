package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"

	"shareme/internal/shortcut"
)

func (s *Service) setupShortcut(w http.ResponseWriter, r *http.Request, run *serverRun) {
	device, _ := r.Context().Value(deviceContextKey{}).(string)
	if run.host != "peer.shareme" || !validHex(device, 16) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "pair this browser before connecting its Shortcut"})
		return
	}
	if err := requirePreflightHeader(r); err != nil {
		s.respondError(w, err, "")
		return
	}
	if s.config.ShortcutSetup == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Shortcut setup is unavailable in this build"})
		return
	}
	setup, err := s.config.ShortcutSetup(device)
	if err != nil {
		log.Printf("set up Shortcut: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Could not connect the Shortcut. Try again or check Share Me on Windows.",
		})
		return
	}
	writeJSON(w, http.StatusOK, setup)
}

// HandleShortcut is an internal adapter for an authenticated SSH connection.
// It cannot approve transfers or reach outbox, setup, or arbitrary HTTP routes.
func (s *Service) HandleShortcut(ctx context.Context, input shortcut.Request) (shortcut.Response, error) {
	if !validHex(input.DeviceID, 16) {
		return shortcut.Response{}, errors.New("authenticated Shortcut device is required")
	}
	s.mu.Lock()
	run, running := s.run, s.running
	s.mu.Unlock()
	if !running || run == nil || run.host != "peer.shareme" {
		return shortcut.Response{}, errors.New("encrypted receiving is not running")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(run.ctx, cancel)
	defer stop()
	defer cancel()
	ctx = context.WithValue(ctx, deviceContextKey{}, input.DeviceID)
	method, path, contentType := http.MethodPost, "", "application/json"
	var body io.Reader
	if input.Body == nil {
		input.Body = strings.NewReader("")
	}
	if input.Operation != "request" &&
		(!validHex(input.ID, 16) || len(input.Token) != 43 || strings.ContainsAny(input.Token, "\r\n")) {
		return shortcut.Response{}, errors.New("invalid Shortcut approval credentials")
	}
	switch input.Operation {
	case "request":
		path, body = "/api/request", input.Body
	case "status", "cancel":
		path, method = "/api/request/"+input.ID, http.MethodGet
		if input.Operation == "cancel" {
			method = http.MethodDelete
		}
	case "text":
		text, err := io.ReadAll(io.LimitReader(input.Body, maxTextBytes+1))
		if err != nil {
			return shortcut.Response{}, fmt.Errorf("read Shortcut text: %w", err)
		}
		if len(text) > maxTextBytes {
			return shortcut.Response{}, errors.New("text is limited to 64 KB")
		}
		if err := validateText(string(text)); err != nil {
			return shortcut.Response{}, err
		}
		encoded, err := json.Marshal(map[string]string{"text": string(text)})
		if err != nil {
			return shortcut.Response{}, err
		}
		path, body = "/api/text", bytes.NewReader(encoded)
	case "upload":
		name, err := validatedFilename(input.Name)
		if err != nil {
			return shortcut.Response{}, err
		}
		var envelope bytes.Buffer
		writer := multipart.NewWriter(&envelope)
		if _, err := writer.CreateFormFile("file", name); err != nil {
			return shortcut.Response{}, fmt.Errorf("create Shortcut upload header: %w", err)
		}
		prefix := envelope.String()
		envelope.Reset()
		if err := writer.Close(); err != nil {
			return shortcut.Response{}, fmt.Errorf("complete Shortcut upload framing: %w", err)
		}
		path, contentType = "/api/upload", writer.FormDataContentType()
		body = io.MultiReader(strings.NewReader(prefix), input.Body, strings.NewReader(envelope.String()))
	default:
		return shortcut.Response{}, errors.New("unsupported Shortcut operation")
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://peer.shareme"+path, body)
	if err != nil {
		return shortcut.Response{}, err
	}
	request.RemoteAddr = input.RemoteAddr
	request.Header.Set("X-Share-Me", "1")
	request.Header.Set("Content-Type", contentType)
	if input.Operation != "request" {
		request.Header.Set("X-Share-Me-Request", input.ID)
		request.Header.Set("X-Share-Me-Token", input.Token)
	}
	recorder := &shortcutResponse{header: make(http.Header)}
	s.handler(run).ServeHTTP(recorder, request)
	if recorder.err != nil {
		return shortcut.Response{}, recorder.err
	}
	if !json.Valid(recorder.body.Bytes()) {
		return shortcut.Response{}, errors.New("Shortcut transfer did not produce a JSON receipt")
	}
	return shortcut.Response{StatusCode: recorder.status, Body: recorder.body.Bytes()}, nil
}

type shortcutResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	err    error
}

func (w *shortcutResponse) Header() http.Header { return w.header }
func (w *shortcutResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *shortcutResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(data) > 64<<10 {
		w.err = errors.New("Shortcut response exceeded its size limit")
		return 0, w.err
	}
	return w.body.Write(data)
}
