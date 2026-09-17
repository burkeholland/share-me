package transfer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type PendingTransfer struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Source    string    `json:"source"`
	Preview   string    `json:"preview"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
}

type pendingRequest struct {
	transfer  PendingTransfer
	ctx       context.Context
	until     time.Time
	decision  chan bool
	decided   bool
	preflight *preflightState
}

// Pending is native-only. Preview is plain text, never HTML. Entries remain
// visible while receiving and scanning, and disappear when the request ends.
func (s *Service) Pending() []PendingTransfer {
	s.mu.Lock()
	result := make([]PendingTransfer, 0, len(s.pending))
	for _, request := range s.pending {
		result = append(result, request.transfer)
	}
	s.mu.Unlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

// Decide applies once to a direct request or its short-lived preflight. An
// accepted preflight permits one matching transfer, never ongoing device trust.
func (s *Service) Decide(id string, accept bool) error {
	s.mu.Lock()
	request, exists := s.pending[id]
	if !exists || request.decided || request.transfer.State != "pending" ||
		!s.running || request.ctx.Err() != nil || !time.Now().Before(request.until) {
		s.mu.Unlock()
		return errors.New("transfer decision is stale: request is no longer awaiting approval")
	}
	request.decided = true
	if accept {
		request.transfer.State = "receiving"
		if request.preflight != nil {
			request.until = time.Now().Add(preflightAcceptanceLifetime)
			s.armPreflightLocked(request)
		}
	} else if request.preflight != nil {
		s.finishPreflightLocked(request, "declined")
	} else {
		delete(s.pending, id)
	}
	if request.preflight == nil {
		request.decision <- accept
	}
	s.mu.Unlock()
	s.notify()
	return nil
}

func remoteIP(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", fmt.Errorf("read transfer source address: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "", fmt.Errorf("read transfer source IP: %w", err)
	}
	return ip.Unmap().String(), nil
}

func (s *Service) makePending(r *http.Request, ctx context.Context, kind, name string, size int64, preview string) (*pendingRequest, error) {
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	source, err := remoteIP(r)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	request := &pendingRequest{
		transfer: PendingTransfer{
			ID: id, Kind: kind, Name: name, Size: size, Source: source,
			Preview: preview, State: "pending", CreatedAt: now.UTC(),
		},
		ctx: ctx, until: now.Add(s.config.ApprovalTimeout), decision: make(chan bool, 1),
	}
	return request, nil
}

func (s *Service) propose(r *http.Request, kind, name string, size int64, preview string) (*pendingRequest, error) {
	request, err := s.makePending(r, r.Context(), kind, name, size, preview)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if err := r.Context().Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !s.running {
		s.mu.Unlock()
		return nil, context.Canceled
	}
	if len(s.pending) >= maxPendingTransfers {
		s.mu.Unlock()
		return nil, problem(http.StatusTooManyRequests, "eight transfers are already pending or active; try again shortly")
	}
	s.pending[request.transfer.ID] = request
	s.mu.Unlock()
	s.notify()
	return request, nil
}

func (s *Service) requestApproval(r *http.Request, kind, name string, size int64, text string) (*pendingRequest, error) {
	if hasPreflightHeaders(r) {
		return s.claimPreflight(r, kind, name, size, text)
	}
	preview := ""
	if kind == "text" {
		preview = textPreview(text)
	}
	request, err := s.propose(r, kind, name, size, preview)
	if err != nil {
		return nil, err
	}
	if err := s.awaitDecision(request); err != nil {
		s.removePending(request)
		return nil, err
	}
	return request, nil
}

func (s *Service) awaitDecision(request *pendingRequest) error {
	timer := time.NewTimer(time.Until(request.until))
	defer timer.Stop()
	var accept bool
	select {
	case <-request.ctx.Done():
		return request.ctx.Err()
	case accept = <-request.decision:
	case <-timer.C:
		s.mu.Lock()
		decided := request.decided
		request.decided = true
		s.mu.Unlock()
		if !decided {
			return problem(http.StatusRequestTimeout, "transfer approval timed out")
		}
		accept = <-request.decision
	}
	if err := request.ctx.Err(); err != nil {
		return err
	}
	if !accept {
		return problem(http.StatusForbidden, "transfer declined on the PC")
	}
	return nil
}

func (s *Service) removePending(request *pendingRequest) {
	s.mu.Lock()
	exists := s.pending[request.transfer.ID] == request
	if exists {
		delete(s.pending, request.transfer.ID)
	}
	s.mu.Unlock()
	if exists {
		s.notify()
	}
}

func (s *Service) scanning(request *pendingRequest) error {
	s.mu.Lock()
	if !s.running || request.ctx.Err() != nil {
		s.mu.Unlock()
		return context.Canceled
	}
	if s.pending[request.transfer.ID] != request || !request.decided || request.transfer.State != "receiving" {
		s.mu.Unlock()
		return errors.New("file is not approved for scanning")
	}
	request.transfer.State = "scanning"
	s.mu.Unlock()
	s.notify()
	return request.ctx.Err()
}

func declaredFileSize(r *http.Request, maximum int64) (int64, error) {
	values := r.Header.Values("X-Share-Me-Size")
	if len(values) == 0 {
		return -1, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, problem(http.StatusBadRequest, "X-Share-Me-Size must be one nonnegative integer")
	}
	for _, value := range values[0] {
		if value < '0' || value > '9' {
			return 0, problem(http.StatusBadRequest, "X-Share-Me-Size must be one nonnegative integer")
		}
	}
	size, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || size > maximum {
		return 0, problem(http.StatusRequestEntityTooLarge, "declared file size exceeds the configured maximum")
	}
	return size, nil
}

func textPreview(text string) string {
	runes := []rune(text)
	if len(runes) > 200 {
		runes = append(runes[:199], '\u2026')
	}
	for i, value := range runes {
		if unicode.IsControl(value) || (value >= '\u202a' && value <= '\u202e') ||
			(value >= '\u2066' && value <= '\u2069') {
			runes[i] = ' '
		}
	}
	return string(runes)
}

// Check both the original Windows basename and the final sanitized name so
// truncation, trailing spaces/dots, or ADS syntax cannot hide a blocked type.
func validatedFilename(original string) (string, error) {
	for _, value := range original {
		if unicode.IsControl(value) {
			return "", problem(http.StatusBadRequest, "filename must not contain control characters")
		}
	}
	name := sanitizeFilename(original)
	base := original[strings.LastIndexAny(original, `/\`)+1:]
	base, _, _ = strings.Cut(base, ":")
	for _, candidate := range []string{base, name} {
		extension := strings.ToLower(filepath.Ext(strings.TrimRight(candidate, ". ")))
		if _, blocked := unsafeExtensions[extension]; blocked {
			return "", problem(http.StatusUnsupportedMediaType, "this file type is not allowed: "+extension)
		}
	}
	return name, nil
}

var unsafeExtensions = func() map[string]struct{} {
	result := make(map[string]struct{})
	for _, extension := range strings.Fields(
		"exe dll com scr cpl msi msp msix msixbundle appx appxbundle bat cmd ps1 psm1 psd1 " +
			"vbs vbe js jse wsf wsh hta lnk url reg chm scf application appref-ms jar iso img " +
			"vhd vhdx dmg html htm xhtml mht mhtml svg pif sct msc gadget diagcab " +
			"py pyw pyc pyo pyd pyz pyzw rb rbw pl pm sh bash zsh fish command php wsc " +
			"docm dotm xlsm xltm xlam xlsb xll pptm potm ppam ppsm sldm " +
			"appinstaller settingcontent-ms library-ms search-ms searchconnector-ms theme rdp") {
		result["."+extension] = struct{}{}
	}
	return result
}()
