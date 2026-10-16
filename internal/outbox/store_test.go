package outbox

import (
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

func TestFileSnapshotsPublishDetectedImageTypes(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		mime string
	}{
		{name: "photo.jpg", data: []byte{0xff, 0xd8, 0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0}, mime: "image/jpeg"},
		{name: "photo.heic", data: []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c', 0, 0, 0, 0, 'm', 'i', 'f', '1'}, mime: "image/heic"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, test.name)
			if err := os.WriteFile(source, test.data, 0600); err != nil {
				t.Fatal(err)
			}
			store, err := New(filepath.Join(dir, "outbox"), 1024)
			if err != nil {
				t.Fatal(err)
			}
			item, err := store.AddFile(context.Background(), phone, source)
			if err != nil {
				t.Fatal(err)
			}
			if item.MIME != test.mime {
				t.Fatalf("MIME = %q, want %q", item.MIME, test.mime)
			}
			reopened, err := New(filepath.Join(dir, "outbox"), 1024)
			if err != nil {
				t.Fatal(err)
			}
			if got := reopened.List(phone)[0].MIME; got != test.mime {
				t.Fatalf("reopened MIME = %q, want %q", got, test.mime)
			}
		})
	}
}
