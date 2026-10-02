package transfer

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRestartCleansOnlyOwnedTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	config := Config{DataDir: filepath.Join(dir, "data"), InboxDir: filepath.Join(dir, "inbox")}
	if _, err := New(config); err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(config.InboxDir, ".shareme-quarantine")
	nested := filepath.Join(quarantine, ".shareme-upload-directory")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(config.InboxDir, "published.jpg")
	preserved := []string{
		published, filepath.Join(config.DataDir, "notes.txt"),
		filepath.Join(config.DataDir, ".shareme-upload-wrong-location"),
		filepath.Join(quarantine, ".shareme-state-wrong-location"),
		filepath.Join(nested, ".shareme-upload-child"),
		filepath.Join(config.InboxDir, ".shareme-upload-not-quarantine"),
	}
	stale := []string{filepath.Join(config.DataDir, ".shareme-state-old"), filepath.Join(quarantine, ".shareme-upload-old.jpg")}
	for _, path := range append(append([]string{}, preserved...), stale...) {
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	state := diskState{Version: 1, Items: []Item{
		{ID: strings.Repeat("a", 32), Kind: "file", Name: "published.jpg", Path: published, Size: 4, CreatedAt: time.Now().UTC()},
		{ID: strings.Repeat("b", 32), Kind: "text", Name: "Text", Text: "keep", Size: 4, CreatedAt: time.Now().UTC()},
	}}
	metadata, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(config.DataDir, "state.json")
	if err := os.WriteFile(statePath, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(config)
	if err != nil || len(reloaded.state.Items) != 2 {
		t.Fatal("restart lost committed history", err)
	}
	for _, path := range stale {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("stale temporary retained", path, err)
		}
	}
	for _, path := range preserved {
		if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
			t.Fatal("unrelated or published file changed", path, err)
		}
	}
	if data, err := os.ReadFile(statePath); err != nil || !bytes.Equal(data, metadata) {
		t.Fatal("startup rewrote durable history", err)
	}
}

func TestRestartDoesNotTraverseQuarantineLinks(t *testing.T) {
	dir := t.TempDir()
	config := Config{DataDir: filepath.Join(dir, "data"), InboxDir: filepath.Join(dir, "inbox")}
	if _, err := New(config); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	path := filepath.Join(target, ".shareme-upload-outside")
	if err := os.WriteFile(path, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(config.InboxDir, ".shareme-quarantine")); err != nil {
		t.Skip("directory symlinks unavailable:", err)
	}
	if _, err := New(config); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "outside" {
		t.Fatal("cleanup traversed a directory link", err)
	}
}

func TestRestartLogsLockedTemporaryWithoutFailing(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows sharing-mode deletion failure")
	}
	config := Config{DataDir: t.TempDir(), InboxDir: t.TempDir()}
	path := filepath.Join(config.DataDir, ".shareme-state-locked")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var output bytes.Buffer
	writer := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(writer)
	if _, err := New(config); err != nil {
		t.Fatal("cleanup failure prevented startup", err)
	}
	if _, err := os.Stat(path); err != nil || !strings.Contains(output.String(), "Remove interrupted transfer file") {
		t.Fatal("locked temporary was not retained and reported", err, output.String())
	}
}
