package shortcut

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestMalformedIdentityFailsClosedWithoutRegeneration(t *testing.T) {
	h := newHarness(t, nil)
	h.server.Close()
	base := h.server.state.copy()
	valid, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	unknownVersion := base.copy()
	unknownVersion.Version = 2
	missingKeys := base.copy()
	missingKeys.Keys = nil
	badPrivate := base.copy()
	badPrivate.Private = []byte("not a private key")
	badPublic := base.copy()
	badPublic.Keys["not-base64"] = deviceA
	badDevice := base.copy()
	keyID, err := publicKeyID(signer(t).PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	badDevice.Keys[keyID] = "../device"
	oversized := base.copy()
	for i := 0; i <= maxKeys; i++ {
		key, err := publicKeyID(signer(t).PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		oversized.Keys[key] = deviceA
	}
	cases := map[string][]byte{
		"truncated":       valid[:len(valid)-1],
		"trailing":        append(append([]byte(nil), valid...), []byte(`{}`)...),
		"duplicate field": bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":2,"version":1`), 1),
		"unknown field":   bytes.Replace(valid, []byte(`"version":1`), []byte(`"unknown":1,"version":1`), 1),
		"too large":       bytes.Repeat([]byte(" "), maxPlainState+1),
		"invalid utf8":    []byte{0xff},
		"duplicate mapping": bytes.Replace(valid, []byte(`"keys":{}`),
			[]byte(`"keys":{"`+keyID+`":"`+deviceA+`","`+keyID+`":"`+deviceB+`"}`), 1),
	}
	for name, state := range map[string]identity{
		"wrong version": unknownVersion, "missing keys": missingKeys, "invalid private key": badPrivate,
		"invalid public key": badPublic, "invalid device": badDevice, "too many keys": oversized,
	} {
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		cases[name] = data
	}
	for name, plain := range cases {
		t.Run(name, func(t *testing.T) {
			sealed, err := h.box.protect(plain)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(h.server.store.path, sealed, 0600); err != nil {
				t.Fatal(err)
			}
			if server, err := newServer(h.server.cfg, h.box); err == nil {
				server.Close()
				t.Fatal("invalid state accepted")
			}
			after, err := os.ReadFile(h.server.store.path)
			if err != nil || !bytes.Equal(after, sealed) {
				t.Fatal("invalid identity was overwritten or regenerated")
			}
		})
	}
	for _, raw := range [][]byte{nil, []byte(`{"version":1}`), bytes.Repeat([]byte{0}, maxSealedState+1)} {
		if err = os.WriteFile(h.server.store.path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if server, err := newServer(h.server.cfg, h.box); err == nil {
			server.Close()
			t.Fatal("plaintext/corrupted identity accepted")
		}
	}
}

func TestFailedPersistenceDoesNotEnrollAndConsumesSignedToken(t *testing.T) {
	h := newHarness(t, nil)
	key := signer(t)
	info, err := h.server.CreateSetup(deviceA, "Phone")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(h.server.store.path)
	if err != nil {
		t.Fatal(err)
	}
	h.box.fail.Store(true)
	client, err := tryDial(h, clientConfig(h, "setup-"+info.Enrollment, ssh.PublicKeys(key)))
	if err == nil {
		defer client.Close()
		if _, _, err = command(client, "shareme-v1 setup", nil); err == nil {
			t.Fatal("ready receipt before durable enrollment")
		}
	}
	h.box.fail.Store(false)
	h.server.mu.Lock()
	_, pending := h.server.tokens[enrollmentID(info.Enrollment)]
	enrolled := len(h.server.state.Keys)
	h.server.mu.Unlock()
	if pending || enrolled != 0 {
		t.Fatal("failed save enrolled key or left a claimed token reusable")
	}
	after, err := os.ReadFile(h.server.store.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed protection changed the previous identity")
	}
	files, err := filepath.Glob(h.server.store.path + ".*.new")
	if err != nil || len(files) != 0 {
		t.Fatal("failed save left intermediate identity files")
	}
}

func TestFailedRevocationStillDeniesAndCanRetry(t *testing.T) {
	h := newHarness(t, nil)
	key := signer(t)
	setup(t, h, deviceA, key)
	before, err := os.ReadFile(h.server.store.path)
	if err != nil {
		t.Fatal(err)
	}
	h.deny(deviceA)
	h.box.fail.Store(true)
	if err = h.server.RevokeDevice(deviceA); err == nil {
		t.Fatal("expected persistence error")
	}
	client, err := tryDial(h, clientConfig(h, "shareme", ssh.PublicKeys(key)))
	if err == nil {
		client.Close()
		t.Fatal("failed disk write restored access")
	}
	h.server.mu.Lock()
	remaining := len(h.server.state.Keys)
	h.server.mu.Unlock()
	if remaining != 0 {
		t.Fatal("failed disk write restored in-memory key")
	}
	stale, _, err := h.server.store.load()
	if err != nil || len(stale.Keys) != 1 {
		t.Fatal("test did not preserve the old durable state")
	}
	h.box.fail.Store(false)
	if err = h.server.RevokeDevice(deviceA); err != nil {
		t.Fatal("retry did not succeed:", err)
	}
	after, err := os.ReadFile(h.server.store.path)
	if err != nil || bytes.Equal(before, after) {
		t.Fatal("retry did not persist revocation")
	}
	state, _, err := h.server.store.load()
	if err != nil || len(state.Keys) != 0 {
		t.Fatal("retry retained revoked key on disk")
	}
}

func TestSetupBoundedAndOnlyAllowedDevice(t *testing.T) {
	h := newHarness(t, nil)
	for _, id := range []string{"", "../device", strings.Repeat("c", 32)} {
		if _, err := h.server.CreateSetup(id, "Phone"); err == nil {
			t.Fatal("unpaired device received setup")
		}
	}
	for _, name := range []string{"", " bad ", "Phone\nname", strings.Repeat("x", 81)} {
		if _, err := h.server.CreateSetup(deviceA, name); err == nil {
			t.Fatal("invalid device name accepted")
		}
	}
	for i := 0; i < maxEnrollments; i++ {
		if _, err := h.server.CreateSetup(deviceA, "Phone"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.server.CreateSetup(deviceA, "Phone"); err == nil {
		t.Fatal("outstanding enrollments not bounded")
	}
	h.server.mu.Lock()
	for key, token := range h.server.tokens {
		token.expires = time.Now().Add(-time.Second)
		h.server.tokens[key] = token
	}
	h.server.mu.Unlock()
	if _, err := h.server.CreateSetup(deviceA, "Phone"); err != nil {
		t.Fatal("expired enrollment slots not reclaimed")
	}
	if err := h.server.RevokeDevice(deviceA); err != nil {
		t.Fatal(err)
	}
	h.server.mu.Lock()
	remaining := len(h.server.tokens)
	h.server.mu.Unlock()
	if remaining != 0 {
		t.Fatal("revocation left enrollment tokens")
	}
}

func TestCallbacksDoNotRunUnderMutexAndChangeCanClose(t *testing.T) {
	dir := testDirectory(t)
	var server *Server
	cfg := Config{
		DataDir: dir, LocalIP: "127.0.0.1", AllowLoopback: true,
		IsDeviceAllowed: func(string) bool {
			_ = server.Status()
			return true
		},
		Handle: func(context.Context, Request) (Response, error) {
			_ = server.Status()
			return Response{}, errors.New("secret payload must not reach OnError")
		},
	}
	change := make(chan struct{}, 8)
	problem := make(chan error, 8)
	cfg.OnChange = func() {
		_ = server.Status()
		select {
		case change <- struct{}{}:
		default:
		}
	}
	cfg.OnError = func(err error) {
		_ = server.Status()
		problem <- err
	}
	var err error
	server, err = newServer(cfg, newTestProtector(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	if err = server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, change)
	h := &harness{server: server}
	key := signer(t)
	setup(t, h, deviceA, key)
	client := dial(t, h, "shareme", key)
	assertExitFailure(t, client, "shareme-v1 request", []byte(`{}`))
	select {
	case err := <-problem:
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("handler error leaked untrusted content")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnError callback deadlocked")
	}
	closed := make(chan struct{})
	var once sync.Once
	// Use a separate server so callback configuration remains immutable.
	closeCfg := cfg
	closeCfg.DataDir = testDirectory(t)
	var closeServer *Server
	closeCfg.OnChange = func() {
		closeServer.Close()
		once.Do(func() { close(closed) })
	}
	closeServer, err = newServer(closeCfg, newTestProtector(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeServer.Close() })
	if err = closeServer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, closed)
}

func TestConfigurationNetworkAndLifecycle(t *testing.T) {
	h := newHarness(t, nil)
	for _, change := range []func(*Config){
		func(c *Config) { c.LocalIP = "0.0.0.0" },
		func(c *Config) { c.LocalIP = "8.8.8.8" },
		func(c *Config) { c.LocalIP = "::1" },
		func(c *Config) { c.AllowLoopback = false },
		func(c *Config) { c.Port = -1 },
		func(c *Config) { c.Port = 65536 },
		func(c *Config) { c.DataDir = "" },
		func(c *Config) { c.IsDeviceAllowed = nil },
		func(c *Config) { c.Handle = nil },
		func(c *Config) { c.MaxFileBytes = maxFileBytes + 1 },
	} {
		cfg := h.server.cfg
		change(&cfg)
		if server, err := newServer(cfg, h.box); err == nil {
			server.Close()
			t.Fatal("invalid configuration accepted")
		}
	}
	if err := h.server.Start(context.Background()); err == nil {
		t.Fatal("started twice")
	}
	if err := h.server.Start(nil); err == nil {
		t.Fatal("nil context accepted")
	}
	status := h.server.Status()
	if !status.Running {
		t.Fatal("incorrect running status")
	}
	for _, ip := range []string{"192.168.1.2", "8.8.8.8", "::1", "0.0.0.0"} {
		if h.server.permittedRemote(&net.TCPAddr{IP: net.ParseIP(ip), Port: 1234}) {
			t.Fatalf("loopback server accepted %s", ip)
		}
	}
	lan := &Server{ip: net.IPv4(192, 168, 1, 10), subnet: &net.IPNet{IP: net.IPv4(192, 168, 1, 0), Mask: net.CIDRMask(24, 32)}}
	for _, ip := range []string{"192.168.2.1", "192.168.1.0", "192.168.1.255", "127.0.0.1", "1.1.1.1"} {
		if lan.permittedRemote(&net.TCPAddr{IP: net.ParseIP(ip)}) {
			t.Fatalf("LAN receiver accepted %s", ip)
		}
	}
	if !lan.permittedRemote(&net.TCPAddr{IP: net.ParseIP("192.168.1.20")}) {
		t.Fatal("LAN receiver rejected subnet host")
	}
	h.server.Close()
	if _, err := net.DialTimeout("tcp4", status.Address, 100*time.Millisecond); err == nil {
		t.Fatal("listener still reachable after Close")
	}
	if err := h.server.Start(context.Background()); err == nil {
		t.Fatal("closed server restarted")
	}
	if _, err := h.server.CreateSetup(deviceA, "Phone"); err == nil {
		t.Fatal("closed server created setup")
	}
}

func TestParentContextCancellationClosesListener(t *testing.T) {
	h := newHarness(t, nil)
	h.server.Close()
	server, err := newServer(h.server.cfg, h.box)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	cancel()
	waitFor(t, func() bool { return !server.Status().Running })
	done := make(chan struct{})
	go func() { server.Close(); close(done) }()
	mustFinish(t, done)
}

func TestStrictCommandParsing(t *testing.T) {
	suffix := " " + jobID + " " + jobToken
	for _, input := range []string{
		"", "shareme-v1", "shareme-v2 request", "shareme-v1 REQUEST", "shareme-v1 request\x00",
		"shareme-v1\u00a0request", "shareme-v1 text" + suffix + " extra",
		"shareme-v1 upload" + suffix + " Zg", "shareme-v1 upload" + suffix + " Zh==",
		"shareme-v1 upload" + suffix + " " + base64.StdEncoding.EncodeToString([]byte{0xff}),
		"shareme-v1 upload" + suffix + " " + base64.StdEncoding.EncodeToString([]byte{'f', 0}),
		"shareme-v1 upload" + suffix + " " + base64.StdEncoding.EncodeToString([]byte("..")),
		"shareme-v1 upload" + suffix + " " + base64.StdEncoding.EncodeToString([]byte("C:relative.bin")),
		"shareme-v1 upload" + suffix + " " + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 256))),
		strings.Repeat("x", maxCommandBytes+1),
	} {
		if _, err := parseCommand(input, false); err == nil {
			t.Fatalf("accepted invalid protocol input %q", input)
		}
	}
	for _, input := range []string{" shareme-v1 setup", "shareme-v1  setup", "shareme-v1 setup\n", "shareme-v1 setup extra"} {
		if _, err := parseCommand(input, true); err == nil {
			t.Fatalf("accepted non-exact setup input %q", input)
		}
	}
	for _, input := range []string{"shareme-v1 request", "\tshareme-v1\trequest\r\n", "shareme-v1 text" + suffix, uploadCommand("写真.bin")} {
		if _, err := parseCommand(input, false); err != nil {
			t.Fatalf("rejected valid protocol input %q: %v", input, err)
		}
	}
}

func TestResponseValidationAndPanicContainment(t *testing.T) {
	for _, name := range []string{"invalid status", "invalid JSON", "invalid UTF8", "oversized", "panic"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, func(context.Context, Request) (Response, error) {
				response := Response{StatusCode: 200, Body: []byte(`{}`)}
				switch name {
				case "invalid status":
					response.StatusCode = 0
				case "invalid JSON":
					response.Body = []byte("not JSON")
				case "invalid UTF8":
					response.Body = []byte{'"', 0xff, '"'}
				case "oversized":
					response.Body = []byte(`"` + strings.Repeat("x", maxResponse) + `"`)
				case "panic":
					panic("secret internal details")
				}
				return response, nil
			})
			key := signer(t)
			setup(t, h, deviceA, key)
			client := dial(t, h, "shareme", key)
			assertExitFailure(t, client, "shareme-v1 request", []byte(`{}`))
		})
	}
}

func TestRateBucket(t *testing.T) {
	now := time.Now()
	b := bucket{tokens: 2, capacity: 2, perSecond: 1, last: now}
	if !b.allow(now) || !b.allow(now) || b.allow(now) {
		t.Fatal("burst limit not enforced")
	}
	if b.allow(now.Add(500*time.Millisecond)) || !b.allow(now.Add(time.Second)) {
		t.Fatal("refill limit not enforced")
	}
}
