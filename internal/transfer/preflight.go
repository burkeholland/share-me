package transfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	preflightAcceptanceLifetime = 10 * time.Minute
	maxPreflightTombstones      = 32
)

type preflightState struct {
	tokenHash [32]byte
	textHash  [32]byte
	claimed   bool
	timer     *time.Timer
}

type preflightTombstone struct {
	id        string
	tokenHash [32]byte
	source    string
	status    string
}

type preflightPayload struct {
	kind string
	name string
	size int64
	text string
}

func requirePreflightHeader(r *http.Request) error {
	values := r.Header.Values("X-Share-Me")
	if len(values) != 1 || values[0] != "1" {
		return problem(http.StatusForbidden, "preflight requests require X-Share-Me: 1")
	}
	return nil
}

func hasPreflightHeaders(r *http.Request) bool {
	return len(r.Header.Values("X-Share-Me-Request")) != 0 || len(r.Header.Values("X-Share-Me-Token")) != 0
}

func preflightDenied() error {
	return problem(http.StatusForbidden, "preflight is missing, invalid, unapproved, expired, or already used")
}

func preflightHash(r *http.Request, token string) [32]byte {
	if device, _ := r.Context().Value(deviceContextKey{}).(string); device != "" {
		return sha256.Sum256([]byte("ShareMe preflight v1\n" + device + "\n" + token))
	}
	return sha256.Sum256([]byte(token))
}

func preflightCredentials(r *http.Request, id string) ([32]byte, string, error) {
	values := r.Header.Values("X-Share-Me-Token")
	if !validHex(id, 16) || len(values) != 1 || len(values[0]) != 43 {
		return [32]byte{}, "", preflightDenied()
	}
	source, err := remoteIP(r)
	if err != nil {
		return [32]byte{}, "", preflightDenied()
	}
	return preflightHash(r, values[0]), source, nil
}

func uploadPreflightCredentials(r *http.Request) (string, [32]byte, string, error) {
	if err := requirePreflightHeader(r); err != nil {
		return "", [32]byte{}, "", err
	}
	ids := r.Header.Values("X-Share-Me-Request")
	if len(ids) != 1 {
		return "", [32]byte{}, "", preflightDenied()
	}
	hash, source, err := preflightCredentials(r, ids[0])
	return ids[0], hash, source, err
}

func (s *Service) readPreflight(w http.ResponseWriter, r *http.Request) (preflightPayload, error) {
	fields, err := readJSONFields(w, r, int64(maxTextBytes*6+64<<10), "kind", "name", "size", "text")
	if err != nil {
		return preflightPayload{}, err
	}
	kind, kindErr := jsonString(fields["kind"])
	name, nameErr := jsonString(fields["name"])
	rawSize := fields["size"]
	if kindErr != nil || nameErr != nil || strings.TrimSpace(name) == "" || len(rawSize) == 0 ||
		(rawSize[0] != '-' && (rawSize[0] < '0' || rawSize[0] > '9')) {
		return preflightPayload{}, problem(http.StatusBadRequest, "request requires string kind/name and integer size")
	}
	var size int64
	if err := json.Unmarshal(rawSize, &size); err != nil || size < -1 {
		return preflightPayload{}, problem(http.StatusBadRequest, "size must be an integer of -1 or greater")
	}
	payload := preflightPayload{kind: kind, name: sanitizeFilename(name), size: size}
	switch kind {
	case "file":
		if _, supplied := fields["text"]; supplied {
			return preflightPayload{}, problem(http.StatusBadRequest, "file preflights must not include text")
		}
		payload.name, err = validatedFilename(name)
		if err != nil {
			return preflightPayload{}, err
		}
		if size > s.config.MaxFileBytes {
			return preflightPayload{}, problem(http.StatusRequestEntityTooLarge, "declared file size exceeds the configured maximum")
		}
		if err := s.requireScanner(); err != nil {
			return preflightPayload{}, err
		}
	case "text":
		text, err := jsonString(fields["text"])
		if err != nil {
			return preflightPayload{}, problem(http.StatusBadRequest, "text preflights require a text string")
		}
		if err := validateText(text); err != nil {
			return preflightPayload{}, err
		}
		if size >= 0 && size != int64(len(text)) {
			return preflightPayload{}, problem(http.StatusBadRequest, "size does not match the supplied UTF-8 text")
		}
		payload.text, payload.size = text, int64(len(text))
	default:
		return preflightPayload{}, problem(http.StatusBadRequest, "kind must be file or text")
	}
	return payload, nil
}

func (s *Service) requestTransfer(w http.ResponseWriter, r *http.Request) {
	if err := requirePreflightHeader(r); err != nil {
		s.respondError(w, err, "")
		return
	}
	if !s.acquireUpload(w) {
		return
	}
	defer func() { <-s.uploads }()
	payload, err := s.readPreflight(w, r)
	if err != nil {
		s.respondError(w, err, "could not read transfer request")
		return
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		s.respondError(w, fmt.Errorf("generate preflight token: %w", err), "could not create transfer request")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	s.mu.Lock()
	run := s.run
	running := s.running
	s.mu.Unlock()
	if !running || run == nil {
		s.respondError(w, context.Canceled, "")
		return
	}
	request, err := s.makePending(r, run.ctx, payload.kind, payload.name, payload.size, textPreview(payload.text))
	if err != nil {
		s.respondError(w, err, "could not create transfer request")
		return
	}
	request.preflight = &preflightState{
		tokenHash: preflightHash(r, token), textHash: sha256.Sum256([]byte(payload.text)),
	}
	s.mu.Lock()
	if !s.running || s.run != run || r.Context().Err() != nil {
		s.mu.Unlock()
		s.respondError(w, context.Canceled, "")
		return
	}
	if len(s.pending) >= maxPendingTransfers {
		s.mu.Unlock()
		s.respondError(w, problem(http.StatusTooManyRequests, "eight transfers are already pending or active; try again shortly"), "")
		return
	}
	s.pending[request.transfer.ID] = request
	s.armPreflightLocked(request)
	s.mu.Unlock()
	s.notify()
	writeJSON(w, http.StatusAccepted, map[string]string{
		"id": request.transfer.ID, "token": token, "status": "pending",
	})
}

// A preflight shares the native pending record. Only its hash and bounded
// metadata live in memory; neither approval nor its token is ever persisted.
func (s *Service) armPreflightLocked(request *pendingRequest) {
	if request.preflight.timer != nil {
		request.preflight.timer.Stop()
	}
	request.preflight.timer = time.AfterFunc(time.Until(request.until), func() {
		s.mu.Lock()
		changed := false
		if s.pending[request.transfer.ID] == request && !request.preflight.claimed && !time.Now().Before(request.until) {
			s.finishPreflightLocked(request, "expired")
			changed = true
		}
		s.mu.Unlock()
		if changed {
			s.notify()
		}
	})
}

func (s *Service) rememberPreflightLocked(request *pendingRequest, status string) {
	tombstone := preflightTombstone{
		id: request.transfer.ID, tokenHash: request.preflight.tokenHash,
		source: request.transfer.Source, status: status,
	}
	if len(s.completed) == maxPreflightTombstones {
		copy(s.completed, s.completed[1:])
		s.completed[len(s.completed)-1] = tombstone
	} else {
		s.completed = append(s.completed, tombstone)
	}
}

func (s *Service) finishPreflightLocked(request *pendingRequest, status string) {
	request.preflight.timer.Stop()
	delete(s.pending, request.transfer.ID)
	s.rememberPreflightLocked(request, status)
}

func (s *Service) cancelPreflightsLocked() {
	for id, request := range s.pending {
		if request.preflight != nil {
			request.preflight.timer.Stop()
			if !request.preflight.claimed {
				delete(s.pending, id)
			}
		}
	}
	s.completed = nil
}

func matchesPreflight(hash, expected [32]byte, source, expectedSource string) bool {
	return subtle.ConstantTimeCompare(hash[:], expected[:]) == 1 && source == expectedSource
}

// Polling discloses only a status. A consumed attempt polls as expired; declined
// and expired tombstones are retained in a bounded FIFO, then become unknown.
func (s *Service) lookupPreflightLocked(id string, hash [32]byte, source string) (*pendingRequest, string, bool, error) {
	if !s.running {
		return nil, "", false, preflightDenied()
	}
	request := s.pending[id]
	if request != nil && request.preflight != nil &&
		matchesPreflight(hash, request.preflight.tokenHash, source, request.transfer.Source) {
		if request.preflight.claimed {
			return request, "expired", false, nil
		}
		if !time.Now().Before(request.until) {
			s.finishPreflightLocked(request, "expired")
			return nil, "expired", true, nil
		}
		if request.decided {
			return request, "accepted", false, nil
		}
		return request, "pending", false, nil
	}
	for _, completed := range s.completed {
		if completed.id == id && matchesPreflight(hash, completed.tokenHash, source, completed.source) {
			return nil, completed.status, false, nil
		}
	}
	return nil, "", false, preflightDenied()
}

func (s *Service) pollTransfer(w http.ResponseWriter, r *http.Request, id string) {
	hash, source, err := preflightCredentials(r, id)
	if err != nil {
		s.respondError(w, err, "")
		return
	}
	s.mu.Lock()
	_, status, changed, err := s.lookupPreflightLocked(id, hash, source)
	s.mu.Unlock()
	if changed {
		s.notify()
	}
	if err != nil {
		s.respondError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (s *Service) cancelTransferRequest(w http.ResponseWriter, r *http.Request, id string) {
	if err := requirePreflightHeader(r); err != nil {
		s.respondError(w, err, "")
		return
	}
	hash, source, err := preflightCredentials(r, id)
	if err != nil {
		s.respondError(w, err, "")
		return
	}
	s.mu.Lock()
	request, status, changed, err := s.lookupPreflightLocked(id, hash, source)
	if err == nil && (status == "pending" || status == "accepted") {
		s.finishPreflightLocked(request, "cancelled")
		status, changed = "cancelled", true
	}
	s.mu.Unlock()
	if changed {
		s.notify()
	}
	if err != nil {
		s.respondError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

// Check before parsing to reject missing, unapproved, and reused tickets
// promptly. Claim checks again under the commit lock to prevent concurrent use.
func (s *Service) checkPreflight(r *http.Request) error {
	if !hasPreflightHeaders(r) {
		return nil
	}
	id, hash, source, err := uploadPreflightCredentials(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	_, status, changed, err := s.lookupPreflightLocked(id, hash, source)
	s.mu.Unlock()
	if changed {
		s.notify()
	}
	if err != nil || status != "accepted" {
		return preflightDenied()
	}
	return nil
}

func (s *Service) claimPreflight(r *http.Request, kind, name string, size int64, text string) (*pendingRequest, error) {
	id, hash, source, err := uploadPreflightCredentials(r)
	if err != nil {
		return nil, err
	}
	textHash := sha256.Sum256([]byte(text))
	s.mu.Lock()
	request, status, changed, err := s.lookupPreflightLocked(id, hash, source)
	defer func() {
		s.mu.Unlock()
		if changed {
			s.notify()
		}
	}()
	if err != nil || status != "accepted" || request == nil {
		return nil, preflightDenied()
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	if request.transfer.Kind != kind ||
		(kind == "file" && request.transfer.Name != name) ||
		(request.transfer.Size >= 0 && size >= 0 && request.transfer.Size != size) ||
		(kind == "text" && subtle.ConstantTimeCompare(textHash[:], request.preflight.textHash[:]) != 1) {
		return nil, problem(http.StatusForbidden, "transfer does not match the approved request")
	}
	request.preflight.claimed = true
	request.preflight.timer.Stop()
	request.ctx = r.Context()
	// Consume before reading file contents or invoking the scanner. Even a
	// subsequently malformed or failed attempt cannot spend this approval twice.
	s.rememberPreflightLocked(request, "expired")
	changed = true
	return request, nil
}
