package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const csp = "default-src 'self';script-src 'self' 'unsafe-inline';style-src 'self';img-src 'self' data:;connect-src 'self';frame-ancestors 'none';form-action 'self'"

type apiError struct {
	status  int
	message string
	cause   error
}

func (err *apiError) Error() string { return err.message }
func (err *apiError) Unwrap() error { return err.cause }

func problem(status int, message string) error {
	return &apiError{status: status, message: message}
}

type approvedFailure struct {
	cause error
}

func (err *approvedFailure) Error() string { return err.cause.Error() }
func (err *approvedFailure) Unwrap() error { return err.cause }

func approvedError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var public *apiError
	if errors.As(err, &public) {
		if public.cause == nil || ctx.Err() != nil {
			return err
		}
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &approvedFailure{cause: err}
}

// One bounded, process-local bucket, not an attacker-controlled per-IP map.
type attemptLimiter struct {
	mu     sync.Mutex
	last   time.Time
	tokens float64
}

func (limiter *attemptLimiter) allow() bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := time.Now()
	if limiter.last.IsZero() {
		limiter.tokens = 8
	} else {
		limiter.tokens = min(8, limiter.tokens+now.Sub(limiter.last).Seconds()/5)
	}
	limiter.last = now
	if limiter.tokens < 1 {
		return false
	}
	limiter.tokens--
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	if status >= 400 {
		// Do not drain an unapproved body before delivering a refusal.
		w.Header().Set("Connection", "close")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Network write failures cannot be turned into another response.
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Service) respondError(w http.ResponseWriter, err error, fallback string) {
	var approved *approvedFailure
	operationalFailure := errors.As(err, &approved)
	if operationalFailure && s.config.OnError != nil {
		nativeError := approved.cause
		var public *apiError
		if errors.As(nativeError, &public) && public.cause != nil {
			nativeError = fmt.Errorf("%s: %w", public.message, public.cause)
		}
		// Status and cleanup are updated before delivering the native error.
		defer s.config.OnError(nativeError)
	}
	if !operationalFailure && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		err = problem(http.StatusRequestTimeout, "transfer was cancelled")
	}
	var public *apiError
	if errors.As(err, &public) {
		if public.status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "5")
		}
		if public.cause != nil {
			s.mu.Lock()
			s.lastError = public.message + ": " + public.cause.Error()
			s.mu.Unlock()
		}
		writeJSON(w, public.status, map[string]string{"error": public.message})
		if public.cause != nil {
			s.notify()
		}
		return
	}
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fallback})
	s.notify()
}

func (s *Service) handler(run *serverRun) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", csp)
		if !run.enter() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "transfer service is stopping"})
			return
		}
		defer run.active.Done()
		if r.Host != run.host || (r.URL.IsAbs() && r.URL.Host != run.host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "unapproved host"})
			return
		}
		if !sameOrigin(r, "http://"+run.host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin requests are not allowed"})
			return
		}
		if run.host == "peer.shareme" {
			if device, _ := r.Context().Value(deviceContextKey{}).(string); !validHex(device, 16) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "paired browser required"})
				return
			}
		}
		if r.Method == http.MethodPost && (r.URL.Path == "/api/upload" || r.URL.Path == "/api/text") {
			if err := requireTransferHeader(r); err != nil {
				s.respondError(w, err, "")
				return
			}
		}
		switch r.URL.Path {
		case "/":
			if requireMethod(w, r, http.MethodGet) {
				s.serveAsset(w, r, "phone.html")
			}
		case "/healthz":
			if requireMethod(w, r, http.MethodGet) {
				writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			}
		case "/api/session":
			if requireMethod(w, r, http.MethodGet) {
				writeJSON(w, http.StatusOK, map[string]int64{"maxFileBytes": s.config.MaxFileBytes})
			}
		case "/api/shortcut/setup":
			if requireMethod(w, r, http.MethodPost) {
				s.setupShortcut(w, r, run)
			}
		case "/api/outbox":
			if requireMethod(w, r, http.MethodGet) {
				s.serveOutbox(w, r, "")
			}
		case "/api/request":
			if requireMethod(w, r, http.MethodPost) {
				s.requestTransfer(w, r)
			}
		case "/api/upload":
			if requireMethod(w, r, http.MethodPost) {
				s.upload(w, r)
			}
		case "/api/text":
			if requireMethod(w, r, http.MethodPost) {
				s.text(w, r)
			}
		default:
			if strings.HasPrefix(r.URL.Path, "/api/outbox/") {
				if r.Method == http.MethodGet || r.Method == http.MethodHead {
					s.serveOutbox(w, r, strings.TrimPrefix(r.URL.Path, "/api/outbox/"))
				} else {
					w.Header().Set("Allow", "GET, HEAD")
					writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				}
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/request/") {
				switch r.Method {
				case http.MethodGet:
					s.pollTransfer(w, r, strings.TrimPrefix(r.URL.Path, "/api/request/"))
				case http.MethodDelete:
					s.cancelTransferRequest(w, r, strings.TrimPrefix(r.URL.Path, "/api/request/"))
				default:
					w.Header().Set("Allow", "GET, DELETE")
					writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				}
				return
			}
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				if requireMethod(w, r, http.MethodGet) {
					s.serveAsset(w, r, strings.TrimPrefix(r.URL.Path, "/"))
				}
				return
			}
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
		}
	})
}

func sameOrigin(r *http.Request, address string) bool {
	origins := r.Header.Values("Origin")
	if len(origins) > 1 || (len(origins) == 1 && origins[0] != address) {
		return false
	}
	sites := r.Header.Values("Sec-Fetch-Site")
	if len(sites) > 1 {
		return false
	}
	if len(sites) == 1 {
		switch strings.ToLower(strings.TrimSpace(sites[0])) {
		case "none", "same-origin", "same-site":
		default:
			return false
		}
	}
	return true
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	return false
}

func requireTransferHeader(r *http.Request) error {
	values := r.Header.Values("X-Share-Me")
	if len(values) == 1 && values[0] == "1" {
		return nil
	}
	// Legacy Shortcuts send Authorization instead. Its presence is only a
	// non-simple CSRF marker, never authentication or permission to save.
	for _, value := range r.Header.Values("Authorization") {
		if strings.TrimSpace(value) != "" {
			return nil
		}
	}
	return problem(http.StatusForbidden, "transfer requests require X-Share-Me: 1 or a nonempty Authorization header")
}

func (s *Service) serveAsset(w http.ResponseWriter, r *http.Request, name string) {
	if s.config.Assets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "phone assets are unavailable"})
		return
	}
	if !fs.ValidPath(name) || strings.Contains(name, `\`) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "asset not found"})
		return
	}
	file, err := s.config.Assets.Open(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "asset not found"})
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "asset not found"})
		return
	}
	var header [512]byte
	n, err := io.ReadFull(file, header[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read phone asset"})
		return
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = http.DetectContentType(header[:n])
	}
	w.Header().Set("Content-Type", contentType)
	if name == "assets/ShareMe.shortcut" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="Share Me.shortcut"`)
	}
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(header[:n])
	_, _ = io.Copy(w, file)
}

func readJSONObject(w http.ResponseWriter, r *http.Request, limit int64, allowed ...string) (map[string]string, error) {
	fields, err := readJSONFields(w, r, limit, allowed...)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(fields))
	for key, raw := range fields {
		value, err := jsonString(raw)
		if err != nil {
			return nil, problem(http.StatusBadRequest, "invalid JSON object: only the documented string fields are accepted")
		}
		result[key] = value
	}
	return result, nil
}

func readJSONFields(w http.ResponseWriter, r *http.Request, limit int64, allowed ...string) (map[string]json.RawMessage, error) {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return nil, problem(http.StatusUnsupportedMediaType, "Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	fields, err := decodeObject(r.Body, allowed...)
	if err != nil {
		return nil, bodyError(err, "invalid JSON object: only the documented fields are accepted")
	}
	return fields, nil
}

func jsonString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || raw[0] != '"' || !utf8.Valid(raw) {
		return "", errors.New("expected string value")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

// Unlike struct decoding this also rejects duplicate keys, case-insensitive
// field aliases, and trailing JSON values. Callers validate each value's type.
func decodeObject(reader io.Reader, allowed ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(reader)
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if first != json.Delim('{') {
		return nil, errors.New("expected object")
	}
	result := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, errors.New("expected field name")
		}
		known := false
		for _, field := range allowed {
			known = known || name == field
		}
		if _, duplicate := result[name]; duplicate || !known {
			return nil, errors.New("unknown or duplicate field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		result[name] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("trailing JSON")
	}
	return result, nil
}

func bodyError(err error, message string) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return problem(http.StatusRequestEntityTooLarge, "request body exceeds the allowed size")
	}
	return problem(http.StatusBadRequest, message)
}
