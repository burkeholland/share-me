// Package outbox stores immutable, explicitly selected snapshots for paired browsers.
package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxItems = 20
const MaxBytes int64 = 10 << 30

type Item struct {
	ID        string    `json:"id"`
	DeviceID  string    `json:"deviceId"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"createdAt"`
}

type Store struct {
	mu       sync.Mutex
	dir      string
	maxFile  int64
	items    []Item
	reserved map[string]int64
}

func validID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && value == strings.ToLower(value)
}

func New(dir string, maxFile int64) (*Store, error) {
	if dir == "" || maxFile < 1 {
		return nil, errors.New("outbox directory and positive file limit are required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, maxFile: maxFile, items: []Item{}, reserved: map[string]int64{}}
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err == nil {
		if err := json.Unmarshal(data, &s.items); err != nil {
			return nil, fmt.Errorf("read outbox index: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var total int64
	seen := map[string]bool{}
	for _, item := range s.items {
		if !validID(item.ID) || !validID(item.DeviceID) || seen[item.ID] ||
			(item.Kind != "file" && item.Kind != "text") || !validName(item.Name) ||
			item.Size < 0 || item.Size > maxFile || item.CreatedAt.IsZero() {
			return nil, errors.New("outbox index contains an invalid item")
		}
		seen[item.ID] = true
		total += item.Size
		info, err := os.Lstat(s.path(item.ID))
		if err != nil || !info.Mode().IsRegular() || info.Size() != item.Size {
			return nil, fmt.Errorf("outbox snapshot %s is missing or changed", item.ID)
		}
	}
	if len(s.items) > MaxItems || total > MaxBytes {
		return nil, errors.New("outbox index exceeds storage limits")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		id := strings.TrimSuffix(strings.TrimPrefix(name, "stage-"), ".tmp")
		staging := strings.HasPrefix(name, "stage-") && strings.HasSuffix(name, ".tmp") && validID(id)
		snapshotID := strings.TrimSuffix(name, ".data")
		orphan := strings.HasSuffix(name, ".data") && validID(snapshotID) && !seen[snapshotID]
		if !entry.IsDir() && (staging || orphan || name == "index.tmp") {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return nil, fmt.Errorf("clean interrupted outbox preparation: %w", err)
			}
		}
	}
	return s, nil
}

func validName(name string) bool {
	return utf8.ValidString(name) && strings.TrimSpace(name) != "" && len(name) <= 1024 &&
		!strings.ContainsAny(name, "/\\\x00\r\n")
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".data") }

func (s *Store) List(device string) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Item, 0, len(s.items))
	for _, item := range s.items {
		if device == "" || item.DeviceID == device {
			items = append(items, item)
		}
	}
	return items
}

func (s *Store) AddFile(ctx context.Context, device, path string) (Item, error) {
	file, err := os.Open(path)
	if err != nil {
		return Item{}, fmt.Errorf("open selected file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Item{}, err
	}
	if !info.Mode().IsRegular() {
		return Item{}, errors.New("choose a regular file, not a folder or device")
	}
	return s.add(ctx, device, "file", filepath.Base(path), info.Size(), file, func() error {
		current, err := file.Stat()
		if err != nil {
			return err
		}
		if current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
			return errors.New("the file changed while being prepared; send it again")
		}
		return nil
	})
}

func (s *Store) AddText(ctx context.Context, device, text string) (Item, error) {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > 64<<10 {
		return Item{}, errors.New("text must be nonblank UTF-8 and at most 64 KB")
	}
	return s.add(ctx, device, "text", "Text", int64(len(text)), strings.NewReader(text), nil)
}

func (s *Store) add(ctx context.Context, device, kind, name string, size int64, source io.Reader, verify func() error) (item Item, result error) {
	if !validID(device) || !validName(name) || size < 0 || size > s.maxFile {
		return Item{}, errors.New("invalid recipient, filename, or file size")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Item{}, err
	}
	id := hex.EncodeToString(random[:])
	s.mu.Lock()
	total := size
	for _, item := range s.items {
		total += item.Size
	}
	for _, reserved := range s.reserved {
		total += reserved
	}
	if len(s.items)+len(s.reserved) >= MaxItems || total > MaxBytes {
		s.mu.Unlock()
		return Item{}, errors.New("outbox is full: remove queued items (20 items / 10 GB maximum)")
	}
	s.reserved[id] = size
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.reserved, id)
		s.mu.Unlock()
	}()
	stage := filepath.Join(s.dir, "stage-"+id+".tmp")
	file, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Item{}, err
	}
	closed := false
	defer func() {
		if !closed {
			result = errors.Join(result, file.Close())
		}
		if err := os.Remove(stage); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("clean outbox staging file: %w", err))
		}
	}()
	n, err := io.Copy(file, io.LimitReader(contextReader{ctx, source}, size+1))
	if err != nil {
		return Item{}, fmt.Errorf("prepare outgoing file: %w", err)
	}
	if n != size {
		return Item{}, errors.New("the file size changed while being prepared")
	}
	if verify != nil {
		if err := verify(); err != nil {
			return Item{}, err
		}
	}
	if err := file.Sync(); err != nil {
		return Item{}, err
	}
	err = file.Close()
	closed = true
	if err != nil {
		return Item{}, err
	}
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	item = Item{ID: id, DeviceID: device, Kind: kind, Name: name, Size: size, CreatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Rename(stage, s.path(id)); err != nil {
		return Item{}, err
	}
	next := append(append([]Item{}, s.items...), item)
	if err := s.save(next); err != nil {
		return Item{}, errors.Join(err, os.Remove(s.path(id)))
	}
	s.items = next
	return item, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (s *Store) Open(device, id string) (Item, *os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if item.ID == id && item.DeviceID == device {
			file, err := os.Open(s.path(id))
			if err != nil {
				return Item{}, nil, err
			}
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() != item.Size {
				closeErr := file.Close()
				return Item{}, nil, errors.Join(errors.New("outbox snapshot is unavailable or changed"), err, closeErr)
			}
			return item, file, nil
		}
	}
	return Item{}, nil, os.ErrNotExist
}

func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, item := range s.items {
		if item.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return os.ErrNotExist
	}
	next := append(append([]Item{}, s.items[:index]...), s.items[index+1:]...)
	// Remove access durably before deleting the snapshot.
	if err := s.save(next); err != nil {
		return err
	}
	s.items = next
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("item removed from queue, but its local snapshot could not be deleted: %w", err)
	}
	return nil
}

func (s *Store) save(items []Item) (result error) {
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, "index.tmp")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(s.dir, "index.json"))
}
