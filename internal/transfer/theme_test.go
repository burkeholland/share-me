package transfer

import (
	"net/http"
	"strings"
	"testing"
)

func TestThemeBootstrapIsTheOnlyAdditionalPublicAsset(t *testing.T) {
	f := newFixture(t, 128)
	result := f.call(t, "GET", "/theme-init.js", "", nil)
	assertStatus(t, result, http.StatusOK)
	if string(result.body) != "console.log('theme')" || !strings.Contains(result.header.Get("Content-Type"), "javascript") {
		t.Fatal("theme bootstrap was not served as JavaScript")
	}
	const wantCSP = "default-src 'self';script-src 'self';style-src 'self';img-src 'self' data:;connect-src 'self';frame-ancestors 'none';form-action 'self'"
	if result.header.Get("Content-Security-Policy") != wantCSP {
		t.Fatal("local CSP changed or still permits inline scripts")
	}
	assertStatus(t, f.call(t, "POST", "/theme-init.js", "", nil), http.StatusMethodNotAllowed)
	for _, path := range []string{"/save-worker.js", "/manifest.webmanifest", "/public/theme-init.js", "/theme-init.js/other", "/icons/share-me-180.png"} {
		assertStatus(t, f.call(t, "GET", path, "", nil), http.StatusNotFound)
	}
}
