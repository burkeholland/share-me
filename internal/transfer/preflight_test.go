package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type preflightTicket struct {
	ID     string `json:"id"`
	Token  string `json:"token"`
	Status string `json:"status"`
}

func (ticket preflightTicket) headers(r *http.Request) {
	r.Header.Set("X-Share-Me", "1")
	r.Header.Set("X-Share-Me-Request", ticket.ID)
	r.Header.Set("X-Share-Me-Token", ticket.Token)
}

func (f *fixture) preflight(t *testing.T, kind, name string, size int64, text string) preflightTicket {
	t.Helper()
	fields := map[string]any{"kind": kind, "name": name, "size": size}
	if kind == "text" {
		fields["text"] = text
	}
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	result := f.call(t, "POST", "/api/request", "application/json", body)
	assertStatus(t, result, http.StatusAccepted)
	object(t, result, "id", "token", "status")
	var ticket preflightTicket
	if err := json.Unmarshal(result.body, &ticket); err != nil {
		t.Fatal(err)
	}
	if !validHex(ticket.ID, 16) || len(ticket.Token) != 43 || ticket.Status != "pending" {
		t.Fatal("invalid preflight receipt")
	}
	return ticket
}

func (f *fixture) poll(t *testing.T, ticket preflightTicket, want string) {
	t.Helper()
	result := f.call(t, "GET", "/api/request/"+ticket.ID, "", nil,
		func(r *http.Request) { r.Header.Set("X-Share-Me-Token", ticket.Token) })
	assertStatus(t, result, http.StatusOK)
	fields := object(t, result, "status")
	var status string
	if err := json.Unmarshal(fields["status"], &status); err != nil || status != want {
		t.Fatalf("poll status = %q, want %q (%v)", status, want, err)
	}
}

func TestPreflightTextSharesDecisionAndBindsPayload(t *testing.T) {
	f := newFixture(t, 128)
	const text = "  original text\n"
	ticket := f.preflight(t, "text", "Original note", -1, text)
	pending := f.service.Pending()
	if len(pending) != 1 || pending[0].ID != ticket.ID || pending[0].Source != "127.0.0.1" ||
		pending[0].Name != "Original note" || pending[0].Size != int64(len(text)) {
		t.Fatal("preflight did not use the native pending record", pending)
	}
	f.poll(t, ticket, "pending")
	body, _ := json.Marshal(map[string]string{"text": text})
	assertStatus(t, f.call(t, "POST", "/api/text", "application/json", body, ticket.headers), http.StatusForbidden)
	assertUnpublished(t, f)
	if err := f.service.Decide(ticket.ID, true); err != nil {
		t.Fatal(err)
	}
	f.poll(t, ticket, "accepted")
	if f.service.Pending()[0].State != "receiving" {
		t.Fatal("accepted preflight did not share the existing native state")
	}
	different, _ := json.Marshal(map[string]string{"text": strings.Replace(text, "original", "modified", 1)})
	assertStatus(t, f.call(t, "POST", "/api/text", "application/json", different, ticket.headers), http.StatusForbidden)
	assertUnpublished(t, f)
	item := receiptItem(t, f, f.call(t, "POST", "/api/text", "application/json", body, ticket.headers))
	if item.ID != ticket.ID || item.Text != text || item.Name != "Original note" {
		t.Fatal("approved metadata or text changed", item)
	}
	waitPending(t, f, 0)
	assertStatus(t, f.call(t, "POST", "/api/text", "application/json", body, ticket.headers), http.StatusForbidden)
	f.poll(t, ticket, "expired")
	if err := f.service.Decide(ticket.ID, true); err == nil {
		t.Fatal("consumed preflight accepted another decision")
	}
}

func TestPreflightFileMetadataAndSingleUseScanner(t *testing.T) {
	var scans atomic.Int32
	f := newFixture(t, 128, func(config *Config) {
		config.ScanFile = func(context.Context, string) error { scans.Add(1); return nil }
	})
	ticket := f.preflight(t, "file", "旅行.jpg", 5, "")
	assertUnpublished(t, f)
	if err := f.service.Decide(ticket.ID, true); err != nil {
		t.Fatal(err)
	}
	f.poll(t, ticket, "accepted")
	assertUnpublished(t, f)
	wrongName, contentType := multipartBytes(t, partSpec{field: "file", name: "different.jpg", data: "photo", file: true})
	assertStatus(t, f.call(t, "POST", "/api/upload", contentType, wrongName, ticket.headers), http.StatusForbidden)
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "旅行.jpg", data: "photo", file: true})
	assertStatus(t, f.call(t, "POST", "/api/upload", contentType, body, ticket.headers,
		func(r *http.Request) { r.Header.Set("X-Share-Me-Size", "6") }), http.StatusForbidden)
	assertStatus(t, f.call(t, "POST", "/api/text", "application/json", []byte(`{"text":"photo"}`), ticket.headers), http.StatusForbidden)
	if scans.Load() != 0 {
		t.Fatal("wrong metadata or preflight alone invoked scanning")
	}
	assertUnpublished(t, f)
	item := receiptItem(t, f, f.call(t, "POST", "/api/upload", contentType, body, ticket.headers))
	if item.ID != ticket.ID || item.Name != "旅行.jpg" || item.Size != 5 || scans.Load() != 1 {
		t.Fatal("preflight upload bypassed or duplicated the existing pipeline")
	}
	assertStatus(t, f.call(t, "POST", "/api/upload", contentType, body, ticket.headers), http.StatusForbidden)
	f.poll(t, ticket, "expired")
}

func TestPreflightFileBodyMustMatchKnownSizeAndFailedAttemptIsSpent(t *testing.T) {
	f := newFixture(t, 128)
	ticket := f.preflight(t, "file", "photo.jpg", 5, "")
	if err := f.service.Decide(ticket.ID, true); err != nil {
		t.Fatal(err)
	}
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "photo.jpg", data: "longer", file: true})
	assertStatus(t, f.call(t, "POST", "/api/upload", contentType, body, ticket.headers), http.StatusForbidden)
	assertUnpublished(t, f)
	waitPending(t, f, 0)
	f.poll(t, ticket, "expired")
	valid, contentType := multipartBytes(t, partSpec{field: "file", name: "photo.jpg", data: "photo", file: true})
	assertStatus(t, f.call(t, "POST", "/api/upload", contentType, valid, ticket.headers), http.StatusForbidden)
	unknown := f.preflight(t, "file", "photo.jpg", -1, "")
	if f.service.Pending()[0].Size != -1 {
		t.Fatal("unknown file size was not retained")
	}
	if err := f.service.Decide(unknown.ID, true); err != nil {
		t.Fatal(err)
	}
	receiptItem(t, f, f.call(t, "POST", "/api/upload", contentType, valid, unknown.headers))
}

func TestPreflightScannerFailureUsesNativeErrorHookAndCannotRetry(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, 128, func(config *Config) {
		config.ScanFile = func(context.Context, string) error { return errors.New("blocked file") }
		config.OnError = func(error) { calls.Add(1) }
	})
	ticket := f.preflight(t, "file", "photo.jpg", -1, "")
	if err := f.service.Decide(ticket.ID, true); err != nil {
		t.Fatal(err)
	}
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "photo.jpg", data: "photo", file: true})
	assertStatus(t, f.call(t, "POST", "/api/upload", contentType, body, ticket.headers), http.StatusUnprocessableEntity)
	waitFor(t, func() bool { return len(f.service.uploads) == 0 })
	if calls.Load() != 1 {
		t.Fatal("preflight bypassed the accepted-transfer error hook")
	}
	assertUnpublished(t, f)
	assertStatus(t, f.call(t, "POST", "/api/upload", contentType, body, ticket.headers), http.StatusForbidden)
	f.poll(t, ticket, "expired")
}

func TestPreflightTokenIPAndCSRFRequirements(t *testing.T) {
	f := newFixture(t, 128)
	ticket := f.preflight(t, "text", "Text", 4, "note")
	body := []byte(`{"text":"note"}`)
	for _, modify := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("X-Share-Me-Token") },
		func(r *http.Request) { r.Header.Del("X-Share-Me-Request") },
		func(r *http.Request) { r.Header.Set("X-Share-Me-Token", strings.Repeat("x", 43)) },
		func(r *http.Request) { r.Header.Add("X-Share-Me-Token", ticket.Token) },
		func(r *http.Request) { r.Header.Add("X-Share-Me-Request", ticket.ID) },
		func(r *http.Request) { r.Header.Del("X-Share-Me"); r.Header.Set("Authorization", "legacy-marker") },
	} {
		assertStatus(t, f.call(t, "POST", "/api/text", "application/json", body, ticket.headers, modify), http.StatusForbidden)
	}
	for _, token := range []string{"", strings.Repeat("x", 43)} {
		assertStatus(t, f.call(t, "GET", "/api/request/"+ticket.ID, "", nil,
			func(r *http.Request) { r.Header.Set("X-Share-Me-Token", token) }), http.StatusForbidden)
	}
	assertStatus(t, f.call(t, "GET", "/api/request/"+strings.Repeat("0", 32), "", nil,
		func(r *http.Request) { r.Header.Set("X-Share-Me-Token", ticket.Token) }), http.StatusForbidden)
	f.service.mu.Lock()
	run := f.service.run
	f.service.mu.Unlock()
	request := httptest.NewRequest("GET", f.service.Status().Address+"/api/request/"+ticket.ID, nil)
	request.RemoteAddr = "127.0.0.2:10000"
	request.Header.Set("X-Share-Me-Token", ticket.Token)
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	recorder := httptest.NewRecorder()
	f.service.handler(run).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatal("polling accepted a different source IP")
	}
	if err := f.service.Decide(ticket.ID, true); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("POST", f.service.Status().Address+"/api/text", strings.NewReader(string(body)))
	request.RemoteAddr = "127.0.0.2:10000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Share-Me", "1")
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	ticket.headers(request)
	recorder = httptest.NewRecorder()
	f.service.handler(run).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatal("upload accepted a different source IP")
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Host = "attacker.example" },
		func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") },
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		result := f.call(t, "GET", "/api/request/"+ticket.ID, "", nil,
			func(r *http.Request) { r.Header.Set("X-Share-Me-Token", ticket.Token) }, mutate)
		assertStatus(t, result, http.StatusForbidden)
		if result.header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("polling enabled CORS")
		}
	}
	assertUnpublished(t, f)
	f.poll(t, ticket, "accepted")
}

func TestPreflightDeclineExpiryAndAcceptanceWindow(t *testing.T) {
	f := newFixture(t, 128, func(config *Config) { config.ApprovalTimeout = 100 * time.Millisecond })
	declined := f.preflight(t, "text", "Text", -1, "decline")
	if err := f.service.Decide(declined.ID, false); err != nil {
		t.Fatal(err)
	}
	f.poll(t, declined, "declined")
	if err := f.service.Decide(declined.ID, true); err == nil {
		t.Fatal("declined preflight was revived")
	}
	expired := f.preflight(t, "text", "Text", -1, "expire")
	waitPending(t, f, 0)
	f.poll(t, expired, "expired")
	if err := f.service.Decide(expired.ID, true); err == nil {
		t.Fatal("expired preflight was accepted")
	}
	accepted := f.preflight(t, "text", "Text", -1, "later upload")
	if err := f.service.Decide(accepted.ID, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	f.poll(t, accepted, "accepted")
	f.service.mu.Lock()
	record := f.service.pending[accepted.ID]
	remaining := time.Until(record.until)
	record.until = time.Now().Add(-time.Second)
	f.service.mu.Unlock()
	if remaining > 10*time.Minute || remaining < 10*time.Minute-time.Second {
		t.Fatal("accepted preflight did not receive a ten-minute upload window", remaining)
	}
	f.poll(t, accepted, "expired")
	waitPending(t, f, 0)
	assertUnpublished(t, f)
	if _, err := os.Stat(filepath.Join(f.config.DataDir, "state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight state was persisted", err)
	}
}

func TestPreflightConcurrentConsumptionHasOneWinner(t *testing.T) {
	var scans atomic.Int32
	f := newFixture(t, 128, func(config *Config) {
		config.ScanFile = func(context.Context, string) error { scans.Add(1); return nil }
	})
	ticket := f.preflight(t, "file", "photo.jpg", -1, "")
	if err := f.service.Decide(ticket.ID, true); err != nil {
		t.Fatal(err)
	}
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "photo.jpg", data: "photo", file: true})
	first := f.begin(t, "POST", "/api/upload", contentType, body, ticket.headers)
	second := f.begin(t, "POST", "/api/upload", contentType, body, ticket.headers)
	statuses := map[int]int{finish(t, first).status: 1}
	statuses[finish(t, second).status]++
	if statuses[http.StatusCreated] != 1 || statuses[http.StatusForbidden] != 1 || scans.Load() != 1 {
		t.Fatal("preflight was consumed more than once", statuses, scans.Load())
	}
	items, err := f.service.List()
	if err != nil || len(items) != 1 || items[0].ID != ticket.ID {
		t.Fatal("concurrent consumption duplicated metadata", err)
	}
}

// Isolate memory-capacity checks from the independently tested request limiter.
func refillPreflightTestBudget(f *fixture) {
	f.service.limiter.mu.Lock()
	f.service.limiter.last = time.Time{}
	f.service.limiter.mu.Unlock()
}

func TestPreflightCapacityAndBoundedTombstones(t *testing.T) {
	f := newFixture(t, 128)
	var tickets []preflightTicket
	for range maxPendingTransfers {
		tickets = append(tickets, f.preflight(t, "text", "Text", -1, "bounded"))
	}
	for _, ticket := range tickets[:4] {
		if err := f.service.Decide(ticket.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	refillPreflightTestBudget(f)
	body := []byte(`{"kind":"text","name":"Text","size":-1,"text":"extra"}`)
	assertStatus(t, f.call(t, "POST", "/api/request", "application/json", body), http.StatusTooManyRequests)
	if len(f.service.Pending()) != maxPendingTransfers {
		t.Fatal("pending and accepted preflight capacity was not bounded")
	}
	if err := f.service.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Start("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	var first, latest preflightTicket
	for i := range maxPreflightTombstones + 8 {
		refillPreflightTestBudget(f)
		latest = f.preflight(t, "text", "Text", -1, "bounded history")
		if i == 0 {
			first = latest
		}
		if err := f.service.Decide(latest.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	f.service.mu.Lock()
	pendingCount, tombstones := len(f.service.pending), len(f.service.completed)
	f.service.mu.Unlock()
	if pendingCount != 0 || tombstones != maxPreflightTombstones {
		t.Fatal("completed preflight state grew without a bound", pendingCount, tombstones)
	}
	f.poll(t, latest, "declined")
	assertStatus(t, f.call(t, "GET", "/api/request/"+first.ID, "", nil,
		func(r *http.Request) { r.Header.Set("X-Share-Me-Token", first.Token) }), http.StatusForbidden)
	assertUnpublished(t, f)
}

func TestStopRevokesPreflightsAndCallbacksAreOutsideLocks(t *testing.T) {
	var service atomic.Pointer[Service]
	var callbacks atomic.Int32
	f := newFixture(t, 128, func(config *Config) {
		config.OnChange = func() {
			if current := service.Load(); current != nil {
				_ = current.Status()
				_ = current.Pending()
				_, _ = current.List()
				callbacks.Add(1)
			}
		}
	})
	service.Store(f.service)
	pending := f.preflight(t, "text", "Text", -1, "pending")
	accepted := f.preflight(t, "text", "Text", -1, "accepted")
	if err := f.service.Decide(accepted.ID, true); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := f.service.Stop(); err != nil || time.Since(start) > time.Second {
		t.Fatal("Stop waited for polling preflights", err)
	}
	if len(f.service.Pending()) != 0 {
		t.Fatal("Stop retained preflights")
	}
	if err := f.service.Start("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	for _, ticket := range []preflightTicket{pending, accepted} {
		assertStatus(t, f.call(t, "GET", "/api/request/"+ticket.ID, "", nil,
			func(r *http.Request) { r.Header.Set("X-Share-Me-Token", ticket.Token) }), http.StatusForbidden)
		if err := f.service.Decide(ticket.ID, true); err == nil {
			t.Fatal("Stop retained an approval")
		}
	}
	if callbacks.Load() < 5 {
		t.Fatal("preflight lifecycle callbacks were not delivered")
	}
	assertUnpublished(t, f)
}

func TestStrictPreflightValidationAndNoRemoteApproval(t *testing.T) {
	cases := []struct {
		body   string
		status int
	}{
		{`{}`, 400},
		{`{"kind":"file","name":"photo.jpg","size":null}`, 400},
		{`{"kind":"file","name":"photo.jpg","size":-2}`, 400},
		{`{"kind":"file","name":"photo.jpg","size":1.5}`, 400},
		{`{"kind":"file","name":"photo.jpg","size":129}`, 413},
		{`{"kind":"file","name":"unsafe.exe","size":-1}`, 415},
		{`{"kind":"file","name":"unsafe.py","size":-1}`, 415},
		{`{"kind":"file","name":"unsafe.docm","size":-1}`, 415},
		{`{"kind":"file","name":"photo.jpg","size":-1,"text":""}`, 400},
		{`{"kind":"text","name":"Text","size":1}`, 400},
		{`{"kind":"text","name":"Text","size":1,"text":"longer"}`, 400},
		{`{"kind":"text","name":"Text","size":-1,"text":" "}`, 400},
		{`{"kind":"text","name":"Text","size":-1,"text":"note","accept":true}`, 400},
		{`{"kind":"text","kind":"file","name":"Text","size":-1,"text":"note"}`, 400},
		{`{"kind":"clipboard","name":"Text","size":-1,"text":"note"}`, 400},
	}
	for _, test := range cases {
		t.Run(test.body, func(t *testing.T) {
			f := newFixture(t, 128)
			assertStatus(t, f.call(t, "POST", "/api/request", "application/json", []byte(test.body)), test.status)
			if len(f.service.Pending()) != 0 {
				t.Fatal("invalid preflight prompted approval")
			}
			assertUnpublished(t, f)
		})
	}
	f := newFixture(t, 128)
	body := []byte(`{"kind":"text","name":"Text","size":-1,"text":"note"}`)
	assertStatus(t, f.call(t, "POST", "/api/request", "application/json", body,
		func(r *http.Request) { r.Header.Del("X-Share-Me"); r.Header.Set("Authorization", "legacy") }), http.StatusForbidden)
	assertStatus(t, f.call(t, "OPTIONS", "/api/request", "", nil), http.StatusMethodNotAllowed)
	ticket := f.preflight(t, "text", "Text", -1, "note")
	assertStatus(t, f.call(t, "POST", "/api/request/"+ticket.ID, "application/json", []byte(`{"accept":true}`),
		ticket.headers), http.StatusMethodNotAllowed)
	f.poll(t, ticket, "pending")
	assertUnpublished(t, f)
}

func TestCancelPreflightWithdrawsPendingAndAcceptedRequests(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprintf("accepted=%v", accepted), func(t *testing.T) {
			f := newFixture(t, 128)
			ticket := f.preflight(t, "text", "Text", -1, "not sent")
			if accepted {
				if err := f.service.Decide(ticket.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				assertStatus(t, f.call(t, "DELETE", "/api/request/"+ticket.ID, "", nil, ticket.headers), http.StatusOK)
				f.poll(t, ticket, "cancelled")
			}
			if len(f.service.Pending()) != 0 {
				t.Fatal("phone cancellation must remove its Windows prompt")
			}
			if err := f.service.Decide(ticket.ID, true); err == nil {
				t.Fatal("a cancelled request cannot be accepted")
			}
			assertStatus(t, f.call(t, "POST", "/api/text", "application/json", []byte(`{"text":"not sent"}`), ticket.headers), http.StatusForbidden)
			assertUnpublished(t, f)
		})
	}
}

func TestCancellationRequiresTheRequestTokenAndSource(t *testing.T) {
	f := newFixture(t, 128)
	ticket := f.preflight(t, "text", "Text", -1, "mine")
	other := f.preflight(t, "text", "Text", -1, "another sender")
	path := "/api/request/" + ticket.ID
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("X-Share-Me-Token") },
		func(r *http.Request) { r.Header.Set("X-Share-Me-Token", strings.Repeat("x", 43)) },
		func(r *http.Request) { r.Header.Del("X-Share-Me") },
		func(r *http.Request) { r.Header.Set("Origin", "http://untrusted.example") },
	} {
		assertStatus(t, f.call(t, "DELETE", path, "", nil, ticket.headers, mutate), http.StatusForbidden)
		f.poll(t, ticket, "pending")
	}
	request := httptest.NewRequest("DELETE", f.service.Status().Address+path, nil)
	request.Header.Set("X-Share-Me", "1")
	ticket.headers(request)
	request.RemoteAddr = "127.0.0.2:1234"
	response := httptest.NewRecorder()
	f.service.handler(f.service.run).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("another source IP must not cancel a request")
	}
	assertStatus(t, f.call(t, "DELETE", path, "", nil, ticket.headers), http.StatusOK)
	f.poll(t, other, "pending")
	assertUnpublished(t, f)
}
