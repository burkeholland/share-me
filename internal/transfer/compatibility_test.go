package transfer

import (
	"net/http"
	"testing"
)

func TestLegacyAuthorizationIsOnlyACSRFMarker(t *testing.T) {
	for _, route := range []string{"/api/text", "/api/upload"} {
		for _, authorization := range []string{"Bearer expired-shortcut-token", "bogus-value"} {
			t.Run(route+"/"+authorization, func(t *testing.T) {
				f := newFixture(t, 128)
				contentType, body := "application/x-www-form-urlencoded", []byte("text=old+Shortcut")
				if route == "/api/upload" {
					body, contentType = multipartBytes(t, partSpec{field: "file", name: "old-shortcut.jpg", data: "photo", file: true})
				}
				headers := func(r *http.Request) {
					r.Header.Del("X-Share-Me")
					r.Header.Del("Origin")
					r.Header.Set("Authorization", authorization)
				}
				for _, accept := range []bool{false, true} {
					result := f.begin(t, "POST", route, contentType, body, headers)
					pending := waitPending(t, f, 1)[0]
					assertUnpublished(t, f)
					select {
					case <-result:
						t.Fatal("stale or bogus Authorization bypassed Windows approval")
					default:
					}
					if err := f.service.Decide(pending.ID, accept); err != nil {
						t.Fatal(err)
					}
					response := finish(t, result)
					if accept {
						item := receiptItem(t, f, response)
						if item.ID != pending.ID {
							t.Fatal("decision was not bound to the open request")
						}
					} else {
						assertStatus(t, response, http.StatusForbidden)
						assertUnpublished(t, f)
					}
					waitPending(t, f, 0)
				}
			})
		}
	}
}

func TestLegacyAuthorizationPreservesOriginAndHostProtections(t *testing.T) {
	f := newFixture(t, 128)
	for name, change := range map[string]func(*http.Request){
		"foreign-host":   func(r *http.Request) { r.Host = "attacker.example" },
		"foreign-origin": func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") },
		"null-origin":    func(r *http.Request) { r.Header.Set("Origin", "null") },
		"fetch-site":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		t.Run(name, func(t *testing.T) {
			result := f.call(t, "POST", "/api/text", "application/json", []byte(`{"text":"denied"}`),
				func(r *http.Request) {
					r.Header.Del("X-Share-Me")
					r.Header.Del("Origin")
					r.Header.Set("Authorization", "bogus-value")
					change(r)
				})
			assertStatus(t, result, http.StatusForbidden)
			if result.header.Get("Access-Control-Allow-Origin") != "" || len(f.service.Pending()) != 0 {
				t.Fatal("Authorization enabled cross-origin access or prompted approval")
			}
			assertUnpublished(t, f)
		})
	}
	preflight := f.call(t, "OPTIONS", "/api/text", "", nil, func(r *http.Request) {
		r.Header.Del("Origin")
		r.Header.Set("Access-Control-Request-Headers", "authorization")
		r.Header.Set("Access-Control-Request-Method", "POST")
	})
	assertStatus(t, preflight, http.StatusMethodNotAllowed)
	if preflight.header.Get("Access-Control-Allow-Origin") != "" ||
		preflight.header.Get("Access-Control-Allow-Headers") != "" {
		t.Fatal("legacy compatibility enabled CORS")
	}
}
