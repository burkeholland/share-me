package transfer

import (
	"net/http"
	"testing"
)

func TestAuthorizationDoesNotReplaceTransferHeader(t *testing.T) {
	f := newFixture(t, 128)
	for _, route := range []string{"/api/text", "/api/upload"} {
		t.Run(route, func(t *testing.T) {
			contentType, body := "application/json", []byte(`{"text":"denied"}`)
			if route == "/api/upload" {
				body, contentType = multipartBytes(t, partSpec{field: "file", name: "denied.jpg", data: "photo", file: true})
			}
			result := f.call(t, "POST", route, contentType, body, func(r *http.Request) {
				r.Header.Del("X-Share-Me")
				r.Header.Set("Authorization", "Bearer old-credential")
			})
			assertStatus(t, result, http.StatusForbidden)
			if len(f.service.Pending()) != 0 {
				t.Fatal("Authorization prompted transfer approval")
			}
			assertUnpublished(t, f)
		})
	}
}
