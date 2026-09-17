package peer

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Tests inject an authenticated in-memory protector; production has no bypass.
type testProtector struct {
	aead cipher.AEAD
	fail bool
}

func newTestProtector(t *testing.T) *testProtector {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	aead, err := signalAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	return &testProtector{aead: aead}
}
func (b *testProtector) protect(data []byte) ([]byte, error) {
	if b.fail {
		return nil, errors.New("injected persistence failure")
	}
	iv := make([]byte, 12)
	_, _ = rand.Read(iv)
	return b.aead.Seal(iv, iv, data, nil), nil
}
func (b *testProtector) unprotect(data []byte) ([]byte, error) {
	if len(data) < 12 {
		return nil, errors.New("short ciphertext")
	}
	return b.aead.Open(nil, data[:12], data[12:], nil)
}

func testDir(t *testing.T) string {
	t.Helper()
	id, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(".", ".peer-test-"+id)
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Error(err)
		}
	})
	return path
}

func TestIdentityPersistence(t *testing.T) {
	box := newTestProtector(t)
	store := identityStore{path: filepath.Join(testDir(t), "peer.dpapi"), box: box}
	state, key, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	reloaded, second, err := store.load()
	if err != nil || !key.Equal(second) || roomFor(key) != roomFor(second) || reloaded.Version != 1 {
		t.Fatalf("identity changed: %v", err)
	}
	sealed, err := os.ReadFile(store.path)
	if err != nil || bytes.Contains(sealed, state.Private) || bytes.Contains(sealed, []byte(`"private"`)) {
		t.Fatal("plaintext identity on disk")
	}
	next := copyIdentity(state)
	next.Version = 42
	box.fail = true
	if err := store.save(next); err == nil {
		t.Fatal("save should fail")
	}
	box.fail = false
	if loaded, _, err := store.load(); err != nil || loaded.Version != 1 {
		t.Fatalf("failed save damaged previous state: %v", err)
	}
	sealed[len(sealed)-1] ^= 1
	if err := os.WriteFile(store.path, sealed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.load(); err == nil {
		t.Fatal("corrupt state accepted or replaced")
	}
}
