package transfer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type diskState struct {
	Version int    `json:"version"`
	Items   []Item `json:"items"`
	// Preserve old metadata without using it for authorization or rewriting
	// user data on startup. These records can never grant transfer permission.
	LegacyTokens json.RawMessage `json:"tokens,omitempty"`
}

func (state diskState) clone() diskState {
	state.LegacyTokens = append(json.RawMessage(nil), state.LegacyTokens...)
	state.Items = append([]Item(nil), state.Items...)
	return state
}

func (s *Service) load() (result error) {
	path := filepath.Join(s.config.DataDir, "state.json")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = diskState{Version: 1}
		return nil
	}
	if err != nil {
		return fmt.Errorf("open transfer metadata: %w", err)
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var state diskState
	if err := decoder.Decode(&state); err != nil {
		return fmt.Errorf("read transfer metadata: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("transfer metadata contains trailing data")
	}
	if state.Version != 1 {
		return errors.New("unsupported transfer metadata version")
	}
	ids := make(map[string]bool)
	for _, item := range state.Items {
		if !validHex(item.ID, 16) || ids[item.ID] || item.Name == "" || item.Size < 0 || item.CreatedAt.IsZero() {
			return errors.New("invalid transfer-item metadata")
		}
		ids[item.ID] = true
		switch item.Kind {
		case "file":
			if !filepath.IsAbs(item.Path) || !containedPath(s.config.InboxDir, item.Path) || item.Text != "" {
				return errors.New("invalid file path in transfer metadata")
			}
		case "text":
			if item.Path != "" || len(item.Text) > maxTextBytes || int64(len(item.Text)) != item.Size ||
				!utf8.ValidString(item.Text) || strings.TrimSpace(item.Text) == "" {
				return errors.New("invalid text in transfer metadata")
			}
		default:
			return errors.New("invalid transfer kind in metadata")
		}
	}
	s.state = state
	return nil
}

func validHex(value string, bytes int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes
}

// Save under mu: create, flush, close, and replace in the same directory. State
// is committed in memory only after replacement succeeds. Nothing is pruned
// from the item index, even though the desktop's List view is limited to 100.
func (s *Service) saveLocked(state diskState) (result error) {
	file, err := os.CreateTemp(s.config.DataDir, ".shareme-state-")
	if err != nil {
		return fmt.Errorf("create transfer metadata: %w", err)
	}
	name, closed, published := file.Name(), false, false
	defer func() {
		if !closed {
			result = errors.Join(result, file.Close())
		}
		if !published {
			result = errors.Join(result, os.Remove(name))
		}
	}()
	if err := json.NewEncoder(file).Encode(state); err != nil {
		return fmt.Errorf("encode transfer metadata: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush transfer metadata: %w", err)
	}
	err = file.Close()
	closed = true
	if err != nil {
		return fmt.Errorf("close transfer metadata: %w", err)
	}
	if err := os.Rename(name, filepath.Join(s.config.DataDir, "state.json")); err != nil {
		return fmt.Errorf("replace transfer metadata: %w", err)
	}
	published = true
	return nil
}

func containedPath(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func sanitizeFilename(name string) string {
	var builder strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 || strings.ContainsRune(`<>:"/\|?*`, r) {
			r = '_'
		}
		if builder.Len()+utf8.RuneLen(r) > 180 {
			break
		}
		builder.WriteRune(r)
	}
	name = strings.TrimRight(strings.TrimSpace(builder.String()), ". ")
	if name == "" {
		name = "upload"
	}
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	reserved := base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
		base == "CLOCK$" || base == "CONIN$" || base == "CONOUT$"
	if strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT") {
		suffix := base[3:]
		reserved = reserved || (len(suffix) == 1 && suffix[0] >= '1' && suffix[0] <= '9') ||
			suffix == "¹" || suffix == "²" || suffix == "³"
	}
	if reserved {
		name = "_" + name
	}
	return name
}

func (s *Service) storeText(ctx context.Context, id, name, text string) (Item, error) {
	item := Item{ID: id, Kind: "text", Name: name, MIME: "text/plain; charset=utf-8",
		Size: int64(len(text)), Text: text, CreatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	if !s.running {
		return Item{}, context.Canceled
	}
	next := s.state.clone()
	next.Items = append(next.Items, item)
	if err := s.saveLocked(next); err != nil {
		return Item{}, err
	}
	s.state = next
	return item, nil
}

func (s *Service) publishFile(ctx context.Context, temporary string, item Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.running {
		return context.Canceled
	}
	if !containedPath(s.config.InboxDir, item.Path) {
		return errors.New("file destination escaped inbox")
	}
	if _, err := os.Lstat(item.Path); err == nil {
		return errors.New("file destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check file destination: %w", err)
	}
	if err := os.Rename(temporary, item.Path); err != nil {
		return fmt.Errorf("publish received file: %w", err)
	}
	next := s.state.clone()
	next.Items = append(next.Items, item)
	if err := s.saveLocked(next); err != nil {
		return errors.Join(err, os.Remove(item.Path))
	}
	s.state = next
	return nil
}
