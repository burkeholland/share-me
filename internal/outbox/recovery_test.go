package outbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRestartRemovesOnlyUnpublishedOutboxFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.AddText(context.Background(), phone, "keep")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.tmp", "stage-" + otherPhone + ".tmp", otherPhone + ".data", "unrelated.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("interrupted"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(dir, 1024); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.tmp", "stage-" + otherPhone + ".tmp", otherPhone + ".data"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unpublished file not cleaned", name, err)
		}
	}
	for _, name := range []string{item.ID + ".data", "index.json", "unrelated.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("retained file was removed", name, err)
		}
	}
}
