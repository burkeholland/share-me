//go:build windows

package peer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsDPAPI(t *testing.T) {
	store := identityStore{path: filepath.Join(testDir(t), "identity.dpapi"), box: platformProtector{}}
	state, first, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.save(state); err != nil {
		t.Fatalf("atomic DPAPI replacement failed: %v", err)
	}
	_, second, err := store.load()
	if err != nil || !first.Equal(second) {
		t.Fatalf("DPAPI reload: %v", err)
	}
	sealed, _ := os.ReadFile(store.path)
	if bytes.Contains(sealed, state.Private) {
		t.Fatal("unencrypted private key")
	}
	sealed[len(sealed)/2] ^= 0xff
	if _, err := (platformProtector{}).unprotect(sealed); err == nil {
		t.Fatal("DPAPI accepted tampered ciphertext")
	}
}
