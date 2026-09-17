package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type fixture struct {
	service *Service
	config  Config
	client  *http.Client
}

type response struct {
	status int
	header http.Header
	body   []byte
}

type requestResult struct {
	response response
	err      error
}

// Test artifacts stay under the package; no test opens the real user's inbox.
func testDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp(".", ".transfer-test-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(absolute); err != nil {
			t.Error(err)
		}
	})
	return absolute
}

func newFixture(t *testing.T, maxBytes int64, options ...func(*Config)) *fixture {
	t.Helper()
	root := testDirectory(t)
	config := Config{
		DataDir: filepath.Join(root, "data"), InboxDir: filepath.Join(root, "inbox"),
		AllowLoopback: true, MaxFileBytes: maxBytes, ApprovalTimeout: 3 * time.Second,
		ScanFile: func(context.Context, string) error { return nil },
		Assets: fstest.MapFS{
			"phone.html":    &fstest.MapFile{Data: []byte("<html>phone only</html>")},
			"index.html":    &fstest.MapFile{Data: []byte("desktop secret")},
			"assets/app.js": &fstest.MapFile{Data: []byte("console.log('phone')")},
		},
	}
	for _, option := range options {
		option(&config)
	}
	service, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Stop(); err != nil {
			t.Error(err)
		}
	})
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	return &fixture{
		service: service, config: config,
		client: &http.Client{
			Transport: transport, Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (f *fixture) request(t *testing.T, method, route, contentType string, body []byte, change ...func(*http.Request)) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, f.service.Status().Address+route, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Origin", f.service.Status().Address)
	if method == http.MethodPost {
		request.Header.Set("X-Share-Me", "1")
	}
	for _, mutate := range change {
		mutate(request)
	}
	return request
}

func (f *fixture) begin(t *testing.T, method, route, contentType string, body []byte, change ...func(*http.Request)) <-chan requestResult {
	t.Helper()
	request := f.request(t, method, route, contentType, body, change...)
	result := make(chan requestResult, 1)
	go func() {
		resp, err := f.client.Do(request)
		if err != nil {
			result <- requestResult{err: err}
			return
		}
		data, err := io.ReadAll(resp.Body)
		err = errors.Join(err, resp.Body.Close())
		result <- requestResult{response: response{status: resp.StatusCode, header: resp.Header, body: data}, err: err}
	}()
	return result
}

func finish(t *testing.T, result <-chan requestResult) response {
	t.Helper()
	select {
	case received := <-result:
		if received.err != nil {
			t.Fatal(received.err)
		}
		return received.response
	case <-time.After(6 * time.Second):
		t.Fatal("request did not finish")
		return response{}
	}
}

func (f *fixture) call(t *testing.T, method, route, contentType string, body []byte, change ...func(*http.Request)) response {
	t.Helper()
	return finish(t, f.begin(t, method, route, contentType, body, change...))
}

// Exercise the same explicit local decision as Wails; never install a trust
// token or auto-approval hook in the service. Early validation errors pass back.
func (f *fixture) approved(t *testing.T, route, contentType string, body []byte, change ...func(*http.Request)) response {
	t.Helper()
	result := f.begin(t, "POST", route, contentType, body, change...)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case received := <-result:
			if received.err != nil {
				t.Fatal(received.err)
			}
			return received.response
		case <-tick.C:
			for _, pending := range f.service.Pending() {
				if pending.State == "pending" {
					if err := f.service.Decide(pending.ID, true); err != nil {
						t.Fatal(err)
					}
				}
			}
		case <-timer.C:
			t.Fatal("approved transfer did not finish")
			return response{}
		}
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for service condition")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitPending(t *testing.T, f *fixture, count int) []PendingTransfer {
	t.Helper()
	waitFor(t, func() bool { return len(f.service.Pending()) == count })
	return f.service.Pending()
}

func assertStatus(t *testing.T, result response, status int) {
	t.Helper()
	if result.status != status {
		t.Fatalf("status = %d, want %d; body = %s", result.status, status, result.body)
	}
}

func object(t *testing.T, result response, keys ...string) map[string]json.RawMessage {
	t.Helper()
	if result.header.Get("Content-Type") != "application/json" {
		t.Fatalf("non-JSON Content-Type: %s", result.header.Get("Content-Type"))
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(result.body, &value); err != nil {
		t.Fatal(err)
	}
	actual := make([]string, 0, len(value))
	for key := range value {
		actual = append(actual, key)
	}
	sort.Strings(actual)
	sort.Strings(keys)
	if strings.Join(actual, ",") != strings.Join(keys, ",") {
		t.Fatalf("JSON fields = %v, want %v; body = %s", actual, keys, result.body)
	}
	return value
}

type partSpec struct {
	field string
	name  string
	data  string
	file  bool
}

func multipartBytes(t *testing.T, parts ...partSpec) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for _, part := range parts {
		if !part.file {
			if err := writer.WriteField(part.field, part.data); err != nil {
				t.Fatal(err)
			}
			continue
		}
		destination, err := writer.CreateFormFile(part.field, part.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(destination, part.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes(), writer.FormDataContentType()
}

func receiptItem(t *testing.T, f *fixture, result response) Item {
	t.Helper()
	assertStatus(t, result, http.StatusCreated)
	object(t, result, "id", "name", "size", "kind")
	var receipt receipt
	if err := json.Unmarshal(result.body, &receipt); err != nil {
		t.Fatal(err)
	}
	item, err := f.service.Find(receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != receipt.Name || item.Size != receipt.Size || item.Kind != receipt.Kind {
		t.Fatal("receipt does not match desktop metadata")
	}
	return item
}

func assertUnpublished(t *testing.T, f *fixture) {
	t.Helper()
	items, err := f.service.List()
	if err != nil || len(items) != 0 {
		t.Fatal("unapproved/rejected request published metadata", items, err)
	}
	if err := filepath.WalkDir(f.config.InboxDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return fmt.Errorf("unexpected file, including quarantine: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPublicReceiverAndRemovedCredentialRoutes(t *testing.T) {
	f := newFixture(t, 0)
	session := f.call(t, "GET", "/api/session", "", nil)
	assertStatus(t, session, http.StatusOK)
	fields := object(t, session, "maxFileBytes")
	if string(fields["maxFileBytes"]) != fmt.Sprint(defaultMaxFileBytes) || session.header.Get("Set-Cookie") != "" {
		t.Fatal("public receiver info issued credentials or has the wrong size")
	}
	for _, route := range []string{"/api/pair", "/api/shortcut", "/api/logout", "/api/pending", "/api/approve",
		"/api/decide", "/api/list", "/api/inbox", "/api/items", "/api/download", "/api/clipboard", "/api/status"} {
		for _, method := range []string{"GET", "POST"} {
			result := f.call(t, method, route, "application/json", []byte(`{}`))
			assertStatus(t, result, http.StatusNotFound)
			object(t, result, "error")
		}
		assertStatus(t, f.call(t, "POST", route, "", nil,
			func(r *http.Request) { r.Header.Del("X-Share-Me") }), http.StatusNotFound)
	}
	for _, route := range []string{"/api/upload", "/api/text"} {
		assertStatus(t, f.call(t, "GET", route, "", nil), http.StatusMethodNotAllowed)
		assertStatus(t, f.call(t, "OPTIONS", route, "", nil), http.StatusMethodNotAllowed)
	}
	page := f.call(t, "GET", "/", "", nil)
	assertStatus(t, page, http.StatusOK)
	if string(page.body) != "<html>phone only</html>" || strings.Contains(f.service.Status().Address, "#") {
		t.Fatal("phone URL is not a permanent, non-secret receiver address")
	}
	for key, want := range map[string]string{
		"Cache-Control": "no-store", "Referrer-Policy": "no-referrer",
		"X-Content-Type-Options": "nosniff", "Content-Security-Policy": csp,
	} {
		if page.header.Get(key) != want {
			t.Fatalf("missing %s", key)
		}
	}
	assertStatus(t, f.call(t, "GET", "/assets/app.js", "", nil), http.StatusOK)
	object(t, f.call(t, "GET", "/healthz", "", nil), "ok")
	for _, route := range []string{"/index.html", "/phone.html", "/state.json", "/assets/", "/assets/../index.html",
		"/assets/%2e%2e/index.html", "/assets/a%5c..%5cindex.html", "//index.html"} {
		result := f.call(t, "GET", route, "", nil)
		assertStatus(t, result, http.StatusNotFound)
		if result.header.Get("Location") != "" {
			t.Fatal("unexpected path redirect")
		}
	}
}

func TestHostOriginAndMandatoryHeader(t *testing.T) {
	f := newFixture(t, 128)
	for name, mutate := range map[string]func(*http.Request){
		"foreign-host":         func(r *http.Request) { r.Host = "attacker.example" },
		"localhost-host":       func(r *http.Request) { r.Host = "localhost" },
		"foreign-origin":       func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") },
		"origin-trailing":      func(r *http.Request) { r.Header.Set("Origin", f.service.Status().Address+"/") },
		"null-origin":          func(r *http.Request) { r.Header.Set("Origin", "null") },
		"empty-origin":         func(r *http.Request) { r.Header.Set("Origin", "") },
		"duplicate-origin":     func(r *http.Request) { r.Header.Add("Origin", f.service.Status().Address) },
		"cross-site":           func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"combined-site":        func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin, cross-site") },
		"missing-header":       func(r *http.Request) { r.Header.Del("X-Share-Me") },
		"wrong-header":         func(r *http.Request) { r.Header.Set("X-Share-Me", "true") },
		"duplicate-header":     func(r *http.Request) { r.Header.Add("X-Share-Me", "1") },
		"empty-auth-no-header": func(r *http.Request) { r.Header.Set("Authorization", ""); r.Header.Del("X-Share-Me") },
		"blank-auth-no-header": func(r *http.Request) { r.Header.Set("Authorization", " \t "); r.Header.Del("X-Share-Me") },
		"cookie-no-header":     func(r *http.Request) { r.Header.Set("Cookie", "shareme_session=old"); r.Header.Del("X-Share-Me") },
	} {
		t.Run(name, func(t *testing.T) {
			result := f.call(t, "POST", "/api/text", "application/json", []byte(`{"text":"no"}`), mutate)
			assertStatus(t, result, http.StatusForbidden)
			object(t, result, "error")
			if result.header.Get("Access-Control-Allow-Origin") != "" || len(f.service.Pending()) != 0 {
				t.Fatal("denied request enabled CORS or prompted the desktop")
			}
		})
	}
	assertUnpublished(t, f)
	receiptItem(t, f, f.approved(t, "/api/text", "application/x-www-form-urlencoded", []byte("text=Shortcut"),
		func(r *http.Request) { r.Header.Del("Origin") }))
}

func TestExportedJSONContract(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		keys  []string
	}{
		{"status", Status{}, []string{"running", "address", "inboxDir", "maxFileBytes", "error"}},
		{"pending", PendingTransfer{}, []string{"id", "kind", "name", "size", "source", "preview", "state", "createdAt"}},
		{"item", Item{}, []string{"id", "kind", "name", "size", "mime", "text", "createdAt", "path"}},
		{"network", Network{}, []string{"name", "ip"}},
		{"config", Config{}, []string{"dataDir", "inboxDir", "maxFileBytes", "allowLoopback", "approvalTimeout"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			object(t, response{header: http.Header{"Content-Type": {"application/json"}}, body: body}, test.keys...)
		})
	}
}
