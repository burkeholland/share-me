//go:build !windows

package peer

import "testing"

func TestUnsupportedIdentityFailsClosed(t *testing.T) {
	if _, err := (platformProtector{}).protect([]byte("secret")); err == nil {
		t.Fatal("non-Windows production fallback must not persist secrets")
	}
}
