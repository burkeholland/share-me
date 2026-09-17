//go:build !windows

package shortcut

import "testing"

func TestNoPlaintextProductionFallback(t *testing.T) {
	if _, err := (platformProtector{}).protect([]byte("identity")); err == nil {
		t.Fatal("non-Windows plaintext fallback enabled")
	}
	if _, err := (platformProtector{}).unprotect([]byte("identity")); err == nil {
		t.Fatal("non-Windows plaintext fallback enabled")
	}
}
