package transfer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestEachTextRequestRequiresLocalDecision(t *testing.T) {
	f := newFixture(t, 128)
	for _, credentials := range []string{"", "shareme_session=old-browser", "Bearer old-shortcut"} {
		result := f.begin(t, "POST", "/api/text", "application/json", []byte(`{"text":"  hello  "}`),
			func(r *http.Request) {
				if strings.HasPrefix(credentials, "Bearer ") {
					r.Header.Set("Authorization", credentials)
				} else {
					r.Header.Set("Cookie", credentials)
				}
				r.Header.Set("X-Forwarded-For", "203.0.113.99")
				r.Header.Set("X-Share-Me-Name", "Trusted phone")
			})
		pending := waitPending(t, f, 1)[0]
		if pending.Kind != "text" || pending.State != "pending" || pending.Source != "127.0.0.1" ||
			pending.Size != 9 || pending.Preview != "  hello  " {
			t.Fatalf("invalid pending request: %#v", pending)
		}
		assertUnpublished(t, f)
		select {
		case <-result:
			t.Fatal("credentials bypassed approval")
		default:
		}
		if err := f.service.Decide(pending.ID, false); err != nil {
			t.Fatal(err)
		}
		assertStatus(t, finish(t, result), http.StatusForbidden)
		if err := f.service.Decide(pending.ID, true); err == nil {
			t.Fatal("declined request accepted a late decision")
		}
		assertUnpublished(t, f)
	}
	result := f.begin(t, "POST", "/api/text", "application/json", []byte(`{"text":"approved"}`))
	pending := waitPending(t, f, 1)[0]
	if err := f.service.Decide(pending.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Decide(pending.ID, true); err == nil {
		t.Fatal("approval was reusable")
	}
	item := receiptItem(t, f, finish(t, result))
	if item.ID != pending.ID || item.Text != "approved" {
		t.Fatal("decision was not bound to this request")
	}
	waitPending(t, f, 0)
}

func TestApprovedFileStatesAndQuarantine(t *testing.T) {
	scanning := make(chan string, 1)
	release := make(chan struct{})
	var scans atomic.Int32
	f := newFixture(t, 128, func(config *Config) {
		config.ScanFile = func(ctx context.Context, path string) error {
			scans.Add(1)
			file, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			scanning <- path
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "旅行.jpg", data: "photo", file: true})
	result := f.begin(t, "POST", "/api/upload", contentType, body,
		func(r *http.Request) {
			r.Header.Set("X-Share-Me-Size", "5")
			r.Header.Set("Authorization", "Bearer old-shortcut")
			r.Header.Set("Cookie", "shareme_session=old-browser")
		})
	pending := waitPending(t, f, 1)[0]
	if pending.Name != "旅行.jpg" || pending.Size != 5 || pending.Preview != "" || pending.Source != "127.0.0.1" {
		t.Fatalf("wrong file proposal: %#v", pending)
	}
	assertUnpublished(t, f)
	if scans.Load() != 0 {
		t.Fatal("scanner ran before approval")
	}
	if err := f.service.Decide(pending.ID, true); err != nil {
		t.Fatal(err)
	}
	var quarantine string
	select {
	case quarantine = <-scanning:
	case <-time.After(2 * time.Second):
		t.Fatal("scanner did not run")
	}
	current := f.service.Pending()
	if len(current) != 1 || current[0].State != "scanning" || current[0].ID != pending.ID {
		t.Fatal("scanning was not visible to the desktop", current)
	}
	if filepath.Dir(quarantine) == f.config.InboxDir || filepath.Ext(quarantine) != ".jpg" {
		t.Fatal("scanner did not receive a quarantined file with its extension")
	}
	if items, err := f.service.List(); err != nil || len(items) != 0 {
		t.Fatal("unscanned file was published", err)
	}
	close(release)
	item := receiptItem(t, f, finish(t, result))
	if item.ID != pending.ID || item.Name != pending.Name || filepath.Dir(item.Path) != f.config.InboxDir {
		t.Fatal("scanned file was not correctly published", item)
	}
	data, err := os.ReadFile(item.Path)
	if err != nil || string(data) != "photo" {
		t.Fatal("file content changed", err)
	}
	if _, err := os.Stat(quarantine); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("quarantine copy remained after publication", err)
	}
	waitPending(t, f, 0)
}

func TestMissingAndFailingScannerFailClosed(t *testing.T) {
	for _, mode := range []string{"missing", "failed", "removed-by-scanner"} {
		t.Run(mode, func(t *testing.T) {
			var scans atomic.Int32
			f := newFixture(t, 128, func(config *Config) {
				if mode == "missing" {
					config.ScanFile = nil
				} else {
					config.ScanFile = func(_ context.Context, path string) error {
						scans.Add(1)
						if mode == "removed-by-scanner" {
							if err := os.Remove(path); err != nil {
								return err
							}
						}
						return fmt.Errorf("scanner blocked executable signature at %s", path)
					}
				}
			})
			body, contentType := multipartBytes(t, partSpec{field: "file", name: "disguised.jpg", data: "MZ executable", file: true})
			var result response
			if mode == "missing" {
				result = f.call(t, "POST", "/api/upload", contentType, body)
				assertStatus(t, result, http.StatusServiceUnavailable)
				if !bytes.Contains(result.body, []byte("File scanning is unavailable on this PC")) {
					t.Fatal("missing scanner error was not explicit")
				}
			} else {
				result = f.approved(t, "/api/upload", contentType, body)
				assertStatus(t, result, http.StatusUnprocessableEntity)
				if scans.Load() != 1 {
					t.Fatal("scanner was not invoked once after approval")
				}
			}
			object(t, result, "error")
			if bytes.Contains(result.body, []byte(".shareme-quarantine")) || f.service.Status().Error == "" {
				t.Fatal("scanner error leaked local paths or was hidden from native status")
			}
			waitPending(t, f, 0)
			assertUnpublished(t, f)
			receiptItem(t, f, f.approved(t, "/api/text", "application/json", []byte(`{"text":"text is still supported"}`)))
		})
	}
}

func TestUnsafeExtensionsRejectedBeforeApproval(t *testing.T) {
	for _, extension := range strings.Fields("exe dll com scr cpl msi msp msix msixbundle appx appxbundle bat cmd ps1 psm1 psd1 vbs vbe js jse wsf wsh hta lnk url reg chm scf application appref-ms jar iso img vhd vhdx html htm xhtml mht mhtml svg") {
		for _, name := range []string{"file." + extension, "file." + strings.ToUpper(extension) + ". ",
			`C:\folder\file.` + extension, "file." + extension + ":stream", strings.Repeat("a", 200) + "." + extension} {
			_, err := validatedFilename(name)
			var denied *apiError
			if !errors.As(err, &denied) || denied.status != http.StatusUnsupportedMediaType {
				t.Fatalf("unsafe filename accepted: %s (%v)", name, err)
			}
		}
	}
	for _, name := range []string{"photo.jpg.exe", "page.HTML", "drawing.svg", "script.py", "script.pyw", "shell.sh", "macro.docm", "addin.xll", "remote.library-ms"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 128, func(config *Config) {
				config.ScanFile = func(context.Context, string) error { return errors.New("scanner must not run") }
			})
			body, contentType := multipartBytes(t, partSpec{field: "file", name: name, data: "unsafe", file: true})
			assertStatus(t, f.call(t, "POST", "/api/upload", contentType, body), http.StatusUnsupportedMediaType)
			if len(f.service.Pending()) != 0 || f.service.Status().Error != "" {
				t.Fatal("blocked filename triggered approval or scanning")
			}
			assertUnpublished(t, f)
		})
	}
}

func TestApprovalTimeoutAndLateDecision(t *testing.T) {
	for _, kind := range []string{"file", "text"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, 128, func(config *Config) { config.ApprovalTimeout = 100 * time.Millisecond })
			route, contentType, body := "/api/text", "application/json", []byte(`{"text":"timeout"}`)
			if kind == "file" {
				route = "/api/upload"
				body, contentType = multipartBytes(t, partSpec{field: "file", name: "timeout.jpg", data: "photo", file: true})
			}
			result := f.begin(t, "POST", route, contentType, body)
			pending := waitPending(t, f, 1)[0]
			assertStatus(t, finish(t, result), http.StatusRequestTimeout)
			if err := f.service.Decide(pending.ID, true); err == nil {
				t.Fatal("timed-out request accepted a late decision")
			}
			waitPending(t, f, 0)
			assertUnpublished(t, f)
		})
	}
}

type countingReader struct {
	reader io.Reader
	read   atomic.Int64
}

func (reader *countingReader) Read(data []byte) (int, error) {
	n, err := reader.reader.Read(data)
	reader.read.Add(int64(n))
	return n, err
}

func TestApprovalDoesNotReadFileBodyAndHonorsCancellation(t *testing.T) {
	f := newFixture(t, 2<<20)
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "large.jpg", data: strings.Repeat("x", 1<<20), file: true})
	counted := &countingReader{reader: bytes.NewReader(body)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequestWithContext(ctx, "POST", f.service.Status().Address+"/api/upload", counted)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Share-Me", "1")
	request.RemoteAddr = "127.0.0.1:50000"
	recorder := httptest.NewRecorder()
	f.service.mu.Lock()
	run := f.service.run
	f.service.mu.Unlock()
	done := make(chan struct{})
	go func() { f.service.handler(run).ServeHTTP(recorder, request); close(done) }()
	pending := waitPending(t, f, 1)[0]
	if counted.read.Load() > 4096 {
		t.Fatalf("read %d bytes before approval, more than bounded multipart read-ahead", counted.read.Load())
	}
	assertUnpublished(t, f)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("request context cancellation did not unblock pending approval")
	}
	if recorder.Code != http.StatusRequestTimeout {
		t.Fatal("wrong cancellation response", recorder.Code)
	}
	if err := f.service.Decide(pending.ID, true); err == nil {
		t.Fatal("cancelled request accepted a decision")
	}
	assertUnpublished(t, f)
	waitPending(t, f, 0)
}

func dialPartialUpload(t *testing.T, f *fixture) net.Conn {
	t.Helper()
	host := strings.TrimPrefix(f.service.Status().Address, "http://")
	connection, err := net.DialTimeout("tcp4", host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	wire := fmt.Sprintf("POST /api/upload HTTP/1.1\r\nHost: %s\r\nContent-Type: multipart/form-data; boundary=slow\r\nContent-Length: 100000\r\nX-Share-Me: 1\r\n\r\n--slow\r\nContent-Disposition: form-data; name=\"file\"; filename=\"slow.jpg\"\r\n\r\n", host)
	if _, err := io.WriteString(connection, wire); err != nil {
		t.Fatal(err)
	}
	return connection
}

func TestDeclineRespondsWithoutDrainingUnapprovedBody(t *testing.T) {
	f := newFixture(t, 1<<20)
	connection := dialPartialUpload(t, f)
	pending := waitPending(t, f, 1)[0]
	if pending.Size != -1 {
		t.Fatal("Content-Length was incorrectly presented as the file size")
	}
	assertUnpublished(t, f)
	if err := f.service.Decide(pending.ID, false); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: "POST"})
	if err != nil {
		t.Fatal("decline waited for a body that was never sent", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("wrong decline status", response.StatusCode)
	}
	assertUnpublished(t, f)
}

func TestPendingLimitAndOldestFirst(t *testing.T) {
	f := newFixture(t, 128)
	for i := range maxTransfers {
		f.begin(t, "POST", "/api/text", "application/json", []byte(fmt.Sprintf(`{"text":"request %d"}`, i)))
		waitPending(t, f, i+1)
	}
	pending := f.service.Pending()
	for i, request := range pending {
		if request.Preview != fmt.Sprintf("request %d", i) {
			t.Fatal("pending requests are not oldest-first")
		}
	}
	result := f.call(t, "POST", "/api/text", "application/json", []byte(`{"text":"fifth"}`))
	assertStatus(t, result, http.StatusTooManyRequests)
	if len(f.service.Pending()) != 4 || len(f.service.uploads) != 4 {
		t.Fatal("pending limit was not global")
	}
	pending[0].Name = "mutated copy"
	if f.service.Pending()[0].Name == pending[0].Name {
		t.Fatal("Pending exposed mutable state")
	}
	if err := f.service.Stop(); err != nil {
		t.Fatal(err)
	}
	assertUnpublished(t, f)
}

func TestBoundedTransferRateLimit(t *testing.T) {
	f := newFixture(t, 128)
	for range 8 {
		result := f.begin(t, "POST", "/api/text", "application/json", []byte(`{"text":"decline"}`))
		pending := waitPending(t, f, 1)[0]
		if err := f.service.Decide(pending.ID, false); err != nil {
			t.Fatal(err)
		}
		assertStatus(t, finish(t, result), http.StatusForbidden)
	}
	limited := f.call(t, "POST", "/api/text", "application/json", []byte(`{"text":"no prompt"}`))
	assertStatus(t, limited, http.StatusTooManyRequests)
	if limited.header.Get("Retry-After") == "" || len(f.service.Pending()) != 0 {
		t.Fatal("rate-limited request prompted the desktop")
	}
	assertStatus(t, f.call(t, "GET", "/api/session", "", nil), http.StatusOK)
	assertUnpublished(t, f)
}

func TestCallbacksCanReadAndDecideWithoutDeadlock(t *testing.T) {
	var service atomic.Pointer[Service]
	var mu sync.Mutex
	var states []string
	failures := make(chan error, 8)
	f := newFixture(t, 128, func(config *Config) {
		config.OnChange = func() {
			current := service.Load()
			if current == nil {
				return
			}
			_ = current.Status()
			_, _ = current.List()
			pending := current.Pending()
			mu.Lock()
			if len(pending) == 0 {
				states = append(states, "removed")
			} else {
				states = append(states, pending[0].State)
			}
			mu.Unlock()
			for _, request := range pending {
				if request.State == "pending" {
					if err := current.Decide(request.ID, true); err != nil {
						failures <- err
					}
				}
			}
		}
	})
	service.Store(f.service)
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "callback.jpg", data: "photo", file: true})
	receiptItem(t, f, f.call(t, "POST", "/api/upload", contentType, body))
	waitPending(t, f, 0)
	mu.Lock()
	joined := strings.Join(states, ",")
	mu.Unlock()
	for _, state := range []string{"pending", "receiving", "scanning", "removed"} {
		if !strings.Contains(joined, state) {
			t.Fatal("missing callback state", state, joined)
		}
	}
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
}

func TestTextPreviewIsBoundedPlainText(t *testing.T) {
	f := newFixture(t, 128)
	text := "<script>alert(1)</script>\n\u202e" + strings.Repeat("日本語", 100)
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	result := f.begin(t, "POST", "/api/text", "application/json", body)
	pending := waitPending(t, f, 1)[0]
	if utf8.RuneCountInString(pending.Preview) > 200 || strings.ContainsAny(pending.Preview, "\n\u202e") {
		t.Fatal("preview is unbounded or contains control/bidi formatting")
	}
	encoded, err := json.Marshal(pending)
	if err != nil || bytes.Contains(encoded, []byte("<script>")) {
		t.Fatal("preview JSON did not escape HTML", err)
	}
	if err := f.service.Decide(pending.ID, true); err != nil {
		t.Fatal(err)
	}
	item := receiptItem(t, f, finish(t, result))
	if item.Text != text {
		t.Fatal("preview sanitization changed the saved text")
	}
}

func TestScannerMarkOfTheWebSurvivesRename(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("NTFS alternate data streams are Windows-only")
	}
	const mark = "[ZoneTransfer]\r\nZoneId=3\r\n"
	f := newFixture(t, 128, func(config *Config) {
		config.ScanFile = func(_ context.Context, path string) error {
			return os.WriteFile(path+":Zone.Identifier", []byte(mark), 0600)
		}
	})
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "marked.jpg", data: "photo", file: true})
	item := receiptItem(t, f, f.approved(t, "/api/upload", contentType, body))
	actual, err := os.ReadFile(item.Path + ":Zone.Identifier")
	if err != nil || string(actual) != mark {
		t.Fatal("same-volume publication lost Mark-of-the-Web", err)
	}
}
