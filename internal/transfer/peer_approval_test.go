package transfer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPeerApprovalTicketCannotMoveBetweenDevicesAtSameIP(t *testing.T) {
	const owner = "0123456789abcdef0123456789abcdef"
	const stranger = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	f := newFixture(t, 1024)
	call := func(device, method, path, body, token, id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, f.service.Status().Address+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Share-Me", "1")
		if token != "" {
			r.Header.Set("X-Share-Me-Token", token)
			r.Header.Set("X-Share-Me-Request", id)
		}
		r = r.WithContext(context.WithValue(r.Context(), deviceContextKey{}, device))
		w := httptest.NewRecorder()
		f.service.handler(f.service.run).ServeHTTP(w, r)
		return w
	}
	result := call(owner, "POST", "/api/request", `{"kind":"text","name":"Text","size":-1,"text":"private"}`, "", "")
	var ticket struct{ ID, Token string }
	if result.Code != http.StatusAccepted || json.Unmarshal(result.Body.Bytes(), &ticket) != nil {
		t.Fatal("proposal failed", result.Code, result.Body.String())
	}
	for _, method := range []string{"GET", "DELETE"} {
		if response := call(stranger, method, "/api/request/"+ticket.ID, "", ticket.Token, ticket.ID); response.Code != 403 {
			t.Fatal("another device could use an approval ticket", method, response.Code)
		}
	}
	if err := f.service.Decide(ticket.ID, true); err != nil {
		t.Fatal(err)
	}
	if response := call(stranger, "POST", "/api/text", `{"text":"private"}`, ticket.Token, ticket.ID); response.Code != 403 {
		t.Fatal("another device claimed the approved transfer", response.Code)
	}
	if response := call(owner, "POST", "/api/text", `{"text":"private"}`, ticket.Token, ticket.ID); response.Code != 201 {
		t.Fatal("owner could not complete approved transfer", response.Code, response.Body.String())
	}
}
