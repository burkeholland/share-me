package transfer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"shareme/internal/outbox"
)

func TestOutboxRoutesRequireAuthenticatedConnectionIdentity(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const stranger = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store, err := outbox.New(filepath.Join(t.TempDir(), "outbox"), 1024)
	if err != nil {
		t.Fatal(err)
	}

	item, err := store.AddText(context.Background(), owner, "only for my phone")
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, 1024, func(c *Config) { c.Outbox = store })
	// A real TCP client cannot manufacture the listener's private context value.
	for _, route := range []string{"/api/outbox", "/api/outbox/" + item.ID} {
		assertStatus(t, f.call(t, "GET", route, "", nil, func(r *http.Request) {
			r.Header.Set("X-Share-Me-Device", owner)
		}), http.StatusNotFound)
	}
	call := func(device, route, rangeHeader string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", f.service.Status().Address+route, nil)
		r.RemoteAddr = "127.0.0.1:50000"
		r = r.WithContext(context.WithValue(r.Context(), deviceContextKey{}, device))
		if rangeHeader != "" {
			r.Header.Set("Range", rangeHeader)
		}
		w := httptest.NewRecorder()
		f.service.handler(f.service.run).ServeHTTP(w, r)
		return w
	}
	if result := call(stranger, "/api/outbox/"+item.ID, ""); result.Code != 404 {
		t.Fatal("wrong recipient can read queued contents")
	}
	list := call(stranger, "/api/outbox", "")
	if list.Code != 200 || strings.Contains(list.Body.String(), item.Name) {
		t.Fatal("wrong recipient can see metadata")
	}
	list = call(owner, "/api/outbox", "")
	var response map[string][]map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil || len(response["items"]) != 1 {
		t.Fatal("recipient cannot list own offer", err)
	}
	if _, leaked := response["items"][0]["deviceId"]; leaked {
		t.Fatal("internal recipient data leaked")
	}
	partial := call(owner, "/api/outbox/"+item.ID, "bytes=0-3")
	if partial.Code != 206 || partial.Body.String() != "only" ||
		!strings.HasPrefix(partial.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("attachment retry/range support failed")
	}
}
