package outbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const phone = "0123456789abcdef0123456789abcdef"
const otherPhone = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestSnapshotsArePrivateDurableAndImmutable(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "outbox"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "photo.txt")
	if err := os.WriteFile(source, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddFile(context.Background(), phone, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if len(store.List(otherPhone)) != 0 {
		t.Fatal("another phone saw metadata")
	}
	if _, _, err := store.Open(otherPhone, item.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("another phone could read an offer")
	}
	reopened, err := New(filepath.Join(dir, "outbox"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	_, file, err := reopened.Open(phone, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(data) != "original" {
		t.Fatal("queued contents changed with the source file")
	}
	if err := reopened.Remove(item.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.Open(phone, item.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("removed offer remained readable")
	}
}

func TestOutboxValidationAndCancellation(t *testing.T) {
	store, err := New(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}

	for _, device := range []string{"", "../escape", strings.Repeat("G", 32)} {
		if _, err := store.AddText(context.Background(), device, "text"); err == nil {
			t.Fatal("invalid recipient accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.AddText(ctx, phone, "text"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation succeeded: %v", err)
	}
	if len(store.List("")) != 0 || len(store.reserved) != 0 {
		t.Fatal("failed preparation left an item or reservation")
	}
	if _, err := store.AddText(context.Background(), phone, "too much text"); err == nil {
		t.Fatal("size limit not applied")
	}
	if _, _, err := store.Open(phone, "../../escape"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("arbitrary path was accepted")
	}
}

func TestAddDataPreservesClipboardImageAndMIME(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}
	item, err := store.AddData(context.Background(), phone, "Clipboard image.png", "image/png", data)
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != "file" || item.Name != "Clipboard image.png" || item.MIME != "image/png" {
		t.Fatalf("unexpected clipboard item: %+v", item)
	}
	reopened, err := New(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	persisted, file, err := reopened.Open(phone, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MIME != "image/png" || !bytes.Equal(got, data) {
		t.Fatal("clipboard image bytes or MIME type changed")
	}
}
