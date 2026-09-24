package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExistingInboxAndLegacyStateArePreserved(t *testing.T) {
	f := newFixture(t, 128)
	if err := f.service.Stop(); err != nil {
		t.Fatal(err)
	}
	var originals []Item
	for i := range 2 {
		id := fmt.Sprintf("%032x", i+1)
		path := filepath.Join(f.config.InboxDir, id+"-existing.jpg")
		content := fmt.Sprintf("original photo %d", i)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		originals = append(originals, Item{
			ID: id, Kind: "file", Name: "existing.jpg", Size: int64(len(content)),
			MIME: "image/jpeg", CreatedAt: time.Now().UTC(), Path: path,
		})
	}
	const browser = "old-browser-secret"
	const token = "old-api-secret"
	var records []map[string]any
	for i, secret := range []string{browser, token} {
		hash := sha256.Sum256([]byte(secret))
		kind := "browser"
		if i == 1 {
			kind = "api"
		}
		records = append(records, map[string]any{
			"id": fmt.Sprintf("%032x", i+10), "hash": hex.EncodeToString(hash[:]),
			"kind": kind, "name": "Old phone", "expiresAt": time.Now().Add(30 * 24 * time.Hour),
		})
	}
	legacy, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	state := diskState{Version: 1, Items: originals, LegacyTokens: legacy}
	metadata, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(f.config.DataDir, "state.json")
	if err := os.WriteFile(statePath, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(f.config)
	if err != nil {
		t.Fatal("old version-1 state could not be loaded", err)
	}
	afterLoad, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(afterLoad, metadata) {
		t.Fatal("New rewrote user metadata", err)
	}
	f.service = reloaded
	t.Cleanup(func() { _ = reloaded.Stop() })
	if err := reloaded.Start("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	for _, authorization := range []bool{false, true} {
		result := f.begin(t, "POST", "/api/text", "application/json", []byte(`{"text":"not trusted"}`),
			func(r *http.Request) {
				if authorization {
					r.Header.Set("Authorization", "Bearer "+token)
				} else {
					r.AddCookie(&http.Cookie{Name: "shareme_session", Value: browser})
				}
			})
		pending := waitPending(t, f, 1)[0]
		if err := reloaded.Decide(pending.ID, false); err != nil {
			t.Fatal(err)
		}
		assertStatus(t, finish(t, result), http.StatusForbidden)
	}
	unchanged, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(unchanged, metadata) {
		t.Fatal("rejected transfers changed the old state", err)
	}
	receiptItem(t, f, f.approved(t, "/api/text", "application/json", []byte(`{"text":"new approved text"}`)))
	for i, expected := range originals {
		item, err := reloaded.Find(expected.ID)
		if err != nil || item != expected {
			t.Fatal("existing photo metadata was changed", err)
		}
		content, err := os.ReadFile(item.Path)
		if err != nil || string(content) != fmt.Sprintf("original photo %d", i) {
			t.Fatal("existing photo was changed or deleted", err)
		}
	}
	if err := reloaded.Stop(); err != nil {
		t.Fatal(err)
	}
	again, err := New(f.config)
	if err != nil || len(again.state.Items) != 3 || !bytes.Equal(again.state.LegacyTokens, legacy) {
		t.Fatal("approved append lost legacy data", err)
	}
	if len(again.Pending()) != 0 {
		t.Fatal("approval grants or pending requests persisted across restart")
	}
}

func TestMetadataFailureRollsBackScannedFileAndText(t *testing.T) {
	f := newFixture(t, 128)
	statePath := filepath.Join(f.config.DataDir, "state.json")
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "rollback.jpg", data: "photo", file: true})
	result := f.approved(t, "/api/upload", contentType, body)
	assertStatus(t, result, http.StatusInternalServerError)
	object(t, result, "error")
	if bytes.Contains(result.body, []byte("state.json")) || f.service.Status().Error == "" {
		t.Fatal("storage error was hidden locally or leaked a path over HTTP")
	}
	assertUnpublished(t, f)
	assertStatus(t, f.approved(t, "/api/text", "application/json", []byte(`{"text":"rollback"}`)), http.StatusInternalServerError)
	assertUnpublished(t, f)
	entries, err := os.ReadDir(f.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".shareme-state-") {
			t.Fatal("failed save left partial metadata")
		}
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	receiptItem(t, f, f.approved(t, "/api/upload", contentType, body))
}

func TestListCapsViewNotDurableIndexAndConcurrentWrites(t *testing.T) {
	f := newFixture(t, 128)
	var wait sync.WaitGroup
	failures := make(chan error, 110)
	for i := range 110 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			id, err := randomID()
			if err == nil {
				_, err = f.service.storeText(t.Context(), id, "Text", fmt.Sprintf("text %d", i))
			}
			if err != nil {
				failures <- err
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	items, err := f.service.List()
	if err != nil || len(items) != 100 {
		t.Fatal("List must return the latest 100", err)
	}
	for i := 1; i < len(items); i++ {
		if items[i].CreatedAt.After(items[i-1].CreatedAt) {
			t.Fatal("List is not newest-first")
		}
	}
	reloaded, err := New(f.config)
	if err != nil || len(reloaded.state.Items) != 110 {
		t.Fatal("view cap or concurrent writes lost durable data", err)
	}
	if _, err := reloaded.Find(reloaded.state.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Find("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("Find missing ID did not return not-exist error")
	}
	items[0].Text = "mutated"
	unchanged, err := f.service.Find(items[0].ID)
	if err != nil || unchanged.Text == "mutated" {
		t.Fatal("List leaked mutable service state")
	}
}

func TestConfigAndMetadataValidation(t *testing.T) {
	root := testDirectory(t)
	for _, config := range []Config{
		{}, {DataDir: root, MaxFileBytes: -1}, {DataDir: root, MaxFileBytes: math.MaxInt64},
		{DataDir: root, ApprovalTimeout: -time.Second},
	} {
		if _, err := New(config); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	service, err := New(Config{DataDir: root})
	if err != nil || service.config.ApprovalTimeout != 2*time.Minute {
		t.Fatal("default approval timeout must be two minutes", err)
	}
	for _, data := range []string{`{`, `{"version":2}`, `{"version":1} {}`, `{"version":1,"unknown":1}`,
		`{"version":1,"items":[{"id":"bad"}]}`} {
		if err := os.WriteFile(filepath.Join(root, "state.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(Config{DataDir: root}); err == nil {
			t.Fatalf("invalid metadata silently accepted: %s", data)
		}
	}
}
