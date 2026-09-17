package transfer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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
	"testing/fstest"
	"time"

	"golang.org/x/crypto/ssh"
	"shareme/internal/shortcut"
)

type shortcutTestListener struct {
	done chan struct{}
	once sync.Once
}

func (l *shortcutTestListener) Accept() (net.Conn, error) {
	<-l.done
	return nil, net.ErrClosed
}
func (l *shortcutTestListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}
func (l *shortcutTestListener) Addr() net.Addr { return shortcutTestAddress{} }

type shortcutTestAddress struct{}

func (shortcutTestAddress) Network() string { return "peer" }
func (shortcutTestAddress) String() string  { return "peer.shareme" }

func newShortcutService(t *testing.T, configure ...func(*Config)) *Service {
	t.Helper()
	dir := t.TempDir()
	config := Config{
		DataDir: dir, InboxDir: filepath.Join(dir, "inbox"), MaxFileBytes: 4 << 20,
		ScanFile: func(ctx context.Context, path string) error { return ctx.Err() },
	}
	for _, customize := range configure {
		customize(&config)
	}
	service, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.StartPeer(&shortcutTestListener{done: make(chan struct{})}, "https://example.test/"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Stop(); err != nil {
			t.Error(err)
		}
	})
	return service
}

func TestShortcutSSHUsesApprovalScanningAndDeviceIsolation(t *testing.T) {
	var scans atomic.Int32
	service := newShortcutService(t, func(config *Config) {
		config.ScanFile = func(ctx context.Context, path string) error {
			scans.Add(1)
			if err := ctx.Err(); err != nil {
				return err
			}
			if runtime.GOOS == "windows" {
				return os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0600)
			}
			return ctx.Err()
		}
	})
	const owner = "0123456789abcdef0123456789abcdef"
	const stranger = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server, err := shortcut.New(shortcut.Config{
		DataDir: t.TempDir(), LocalIP: "127.0.0.1", Port: 0, AllowLoopback: true,
		MaxFileBytes: 4 << 20, Handle: service.HandleShortcut,
		IsDeviceAllowed: func(id string) bool { return id == owner || id == stranger },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	call := func(client *ssh.Client, command string, body io.Reader) map[string]any {
		t.Helper()
		session, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		session.Stdin = body
		output, err := session.Output(command)
		if err != nil {
			t.Fatalf("Shortcut operation failed: %v: %s", err, output)
		}
		var result map[string]any
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("Shortcut response is not JSON: %v", err)
		}
		return result
	}
	connect := func(id string) (*ssh.Client, func() *ssh.Client) {
		t.Helper()
		setup, err := server.CreateSetup(id, "Test PC")
		if err != nil {
			t.Fatal(err)
		}
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := ssh.NewSignerFromKey(key)
		if err != nil {
			t.Fatal(err)
		}
		config := &ssh.ClientConfig{
			User: "setup-" + setup.Enrollment, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 5 * time.Second,
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				if ssh.FingerprintSHA256(key) != setup.Fingerprint {
					return fmt.Errorf("PC host fingerprint changed")
				}
				return nil
			},
		}
		client, err := ssh.Dial("tcp", server.Status().Address, config)
		if err != nil {
			t.Fatal(err)
		}
		result := call(client, "shareme-v1 setup", strings.NewReader(""))
		client.Close()
		if result["status"] != "ready" {
			t.Fatal("setup did not complete", result)
		}
		config.User = "shareme"
		reconnect := func() *ssh.Client {
			t.Helper()
			client, err := ssh.Dial("tcp", server.Status().Address, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { client.Close() })
			return client
		}
		return reconnect(), reconnect
	}
	client, reconnect := connect(owner)
	foreign, _ := connect(stranger)
	request := call(client, "shareme-v1 request", strings.NewReader(`{"kind":"file","name":"sample.dat","size":-1}`))
	if request["status"] != "pending" || len(service.Pending()) != 1 {
		t.Fatal("SSH must request Windows approval first", request)
	}
	id, token := request["id"].(string), request["token"].(string)
	client.Close()
	client = reconnect()
	statusCommand := "shareme-v1 status " + id + " " + token
	if result := call(client, statusCommand, nil); result["status"] != "pending" {
		t.Fatal("unexpected pending status", result)
	}
	if scans.Load() != 0 {
		t.Fatal("file scanning ran before approval")
	}
	if err := service.Decide(id, true); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0x00, 0x01, 0xfe, 0xff, 'a', '\n'}, 100000)
	command := "shareme-v1 upload " + id + " " + token + " " + base64.StdEncoding.EncodeToString([]byte("sample.dat"))
	client.Close()
	client = reconnect()
	if result := call(foreign, command, bytes.NewReader(nil)); result["error"] == nil {
		t.Fatal("another phone used the owner's approval")
	}
	result := call(client, command, bytes.NewReader(payload))
	if result["error"] != nil || result["id"] == nil {
		t.Fatal("approved SSH upload did not return a receipt", result)
	}
	items, err := service.List()
	if err != nil || len(items) != 1 {
		t.Fatal("received file missing", err)
	}
	saved, err := os.ReadFile(items[0].Path)
	if err != nil || !bytes.Equal(saved, payload) {
		t.Fatal("raw file bytes changed in SSH/approval adapter", err)
	}
	if scans.Load() != 1 {
		t.Fatal("accepted file did not pass through scanning exactly once")
	}
	if runtime.GOOS == "windows" {
		mark, err := os.ReadFile(items[0].Path + ":Zone.Identifier")
		if err != nil || !bytes.Contains(mark, []byte("ZoneId=3")) {
			t.Fatal("accepted SSH file has no Mark of the Web", err)
		}
	}
	if result := call(client, command, bytes.NewReader(nil)); result["error"] == nil {
		t.Fatal("used approval was accepted again")
	}
	request = call(client, "shareme-v1 request", strings.NewReader(`{"kind":"text","name":"Text","size":-1,"text":"Decline me"}`))
	if err := service.Decide(request["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	statusCommand = "shareme-v1 status " + request["id"].(string) + " " + request["token"].(string)
	if result := call(client, statusCommand, nil); result["status"] != "declined" {
		t.Fatal("decline was not delivered to Shortcut", result)
	}
	request = call(client, "shareme-v1 request", strings.NewReader(`{"kind":"text","name":"Text","size":-1,"text":"A URL: https://example.test/"}`))
	if err := service.Decide(request["id"].(string), true); err != nil {
		t.Fatal(err)
	}
	command = "shareme-v1 text " + request["id"].(string) + " " + request["token"].(string)
	if result := call(client, command, strings.NewReader("A URL: https://example.test/")); result["id"] == nil || result["error"] != nil {
		t.Fatal("approved SSH text failed", result)
	}
	if err := server.RevokeDevice(owner); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewSession(); err == nil {
		t.Fatal("revoked SSH connection remained usable")
	}
}

func TestShortcutAdapterRejectsMissingIdentityAndUnsupportedOperations(t *testing.T) {
	service := newShortcutService(t)
	for _, input := range []shortcut.Request{
		{Operation: "request", Body: strings.NewReader("{}")},
		{DeviceID: "0123456789abcdef0123456789abcdef", Operation: "outbox"},
		{DeviceID: "0123456789abcdef0123456789abcdef", Operation: "setup"},
		{DeviceID: "0123456789abcdef0123456789abcdef", Operation: "upload", ID: "bad", Token: "bad"},
	} {
		if _, err := service.HandleShortcut(context.Background(), input); err == nil {
			t.Fatal("invalid adapter request accepted", input.Operation)
		}
	}
}

func TestShortcutAssetPreservesDownloadNameAndBytes(t *testing.T) {
	const fixture = "Unsigned test fixture only."
	service := newShortcutService(t, func(config *Config) {
		config.Assets = fstest.MapFS{"assets/ShareMe.shortcut": {Data: []byte(fixture)}}
	})
	w := httptest.NewRecorder()
	service.serveAsset(w, httptest.NewRequest(http.MethodGet, "/assets/ShareMe.shortcut", nil), "assets/ShareMe.shortcut")
	if w.Code != http.StatusOK || w.Body.String() != fixture ||
		w.Header().Get("Content-Disposition") != `attachment; filename="Share Me.shortcut"` ||
		w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatal("Shortcut name or content was changed by asset serving")
	}
}

func TestShortcutSetupRequiresPrivateAuthenticatedBrowser(t *testing.T) {
	const device = "0123456789abcdef0123456789abcdef"
	var calls int
	var setupErr error
	service := newShortcutService(t, func(config *Config) {
		config.ShortcutSetup = func(id string) (shortcut.Setup, error) {
			calls++
			if id != device {
				t.Fatal("setup used an untrusted device identity")
			}
			return shortcut.Setup{Version: 1, Host: "192.168.1.2", Port: 49322}, setupErr
		}
	})
	request := func(host, id string, marker bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://"+host+"/api/shortcut/setup", nil)
		if id != "" {
			r = r.WithContext(context.WithValue(r.Context(), deviceContextKey{}, id))
		}
		if marker {
			r.Header.Set("X-Share-Me", "1")
		}
		w := httptest.NewRecorder()
		service.setupShortcut(w, r, &serverRun{host: host})
		return w
	}
	for _, scenario := range []struct {
		host, id string
		marker   bool
	}{
		{"peer.shareme", "", true},
		{"peer.shareme", "forged", true},
		{"192.168.1.2:49321", device, true},
		{"peer.shareme", device, false},
	} {
		if result := request(scenario.host, scenario.id, scenario.marker); result.Code != http.StatusForbidden {
			t.Fatalf("untrusted setup request accepted: %d", result.Code)
		}
	}
	if calls != 0 {
		t.Fatal("untrusted setup request reached the provider")
	}
	service.mu.Lock()
	service.lastError = "an unrelated transfer failure"
	service.mu.Unlock()
	setupErr = errors.New(`private disk error at C:\private\state`)
	if result := request("peer.shareme", device, true); result.Code != http.StatusServiceUnavailable ||
		bytes.Contains(result.Body.Bytes(), []byte("private disk")) {
		t.Fatal("setup failure was hidden or exposed private details")
	}
	setupErr = nil
	if result := request("peer.shareme", device, true); result.Code != http.StatusOK {
		t.Fatal("setup could not recover", result.Body.String())
	}
	if service.Status().Error != "an unrelated transfer failure" {
		t.Fatal("setup overwrote the transfer error state")
	}
}
