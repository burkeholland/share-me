package shortcut

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	deviceA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	deviceB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	jobID   = "0123456789abcdef0123456789abcdef"
)

var jobToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

type testProtector struct {
	aead cipher.AEAD
	fail atomic.Bool
}

func newTestProtector(t *testing.T) *testProtector {
	t.Helper()
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return &testProtector{aead: aead}
}

func (p *testProtector) protect(data []byte) ([]byte, error) {
	if p.fail.Load() {
		return nil, errors.New("test protection failure")
	}
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return p.aead.Seal(nonce, nonce, data, nil), nil
}

func (p *testProtector) unprotect(data []byte) ([]byte, error) {
	if len(data) < p.aead.NonceSize() {
		return nil, errors.New("short test ciphertext")
	}
	return p.aead.Open(nil, data[:p.aead.NonceSize()], data[p.aead.NonceSize():], nil)
}

func testDirectory(t *testing.T) string {
	t.Helper()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(".", ".shortcut-test-"+hex.EncodeToString(random[:]))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

type harness struct {
	server  *Server
	box     *testProtector
	mu      sync.Mutex
	allowed map[string]bool
}

func (h *harness) allow(device string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.allowed[device]
}

func (h *harness) deny(device string) {
	h.mu.Lock()
	delete(h.allowed, device)
	h.mu.Unlock()
}

func newHarness(t *testing.T, handle func(context.Context, Request) (Response, error), customize ...func(*Server)) *harness {
	t.Helper()
	h := &harness{box: newTestProtector(t), allowed: map[string]bool{deviceA: true, deviceB: true}}
	if handle == nil {
		handle = func(_ context.Context, request Request) (Response, error) {
			_, err := io.Copy(io.Discard, request.Body)
			return Response{StatusCode: 200, Body: []byte(`{"status":"ok"}`)}, err
		}
	}
	server, err := newServer(Config{
		DataDir: testDirectory(t), LocalIP: "127.0.0.1", AllowLoopback: true,
		IsDeviceAllowed: h.allow, Handle: handle,
	}, h.box)
	if err != nil {
		t.Fatal(err)
	}
	h.server = server
	for _, fn := range customize {
		fn(server)
	}
	if err = server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return h
}

func signer(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func clientConfig(h *harness, username string, auth ...ssh.AuthMethod) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User: username, Auth: auth, Timeout: 3 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != h.server.Status().Fingerprint {
				return errors.New("host key mismatch")
			}
			return nil
		},
	}
}

func tryDial(h *harness, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	address := h.server.Status().Address
	raw, err := net.DialTimeout("tcp4", address, 3*time.Second)
	if err != nil {
		return nil, err
	}
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	conn, channels, requests, err := ssh.NewClientConn(raw, address, cfg)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return ssh.NewClient(conn, channels, requests), nil
}

func dial(t *testing.T, h *harness, username string, key ssh.Signer) *ssh.Client {
	t.Helper()
	client, err := tryDial(h, clientConfig(h, username, ssh.PublicKeys(key)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func command(client *ssh.Client, command string, stdin []byte) (string, string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", "", err
	}
	defer session.Close()
	var out, stderr bytes.Buffer
	session.Stdout, session.Stderr, session.Stdin = &out, &stderr, bytes.NewReader(stdin)
	err = session.Run(command)
	return out.String(), stderr.String(), err
}

func setup(t *testing.T, h *harness, device string, key ssh.Signer) Setup {
	t.Helper()
	info, err := h.server.CreateSetup(device, "TEST-PC")
	if err != nil {
		t.Fatal(err)
	}
	client := dial(t, h, "setup-"+info.Enrollment, key)
	defer client.Close()
	out, stderr, err := command(client, "shareme-v1 setup", nil)
	if err != nil || out != "{\"status\":\"ready\"}\n" || stderr != "" {
		t.Fatalf("setup: %q / %q / %v", out, stderr, err)
	}
	return info
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func mustFinish(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not stop")
	}
}

func assertExitFailure(t *testing.T, client *ssh.Client, script string, stdin []byte) {
	t.Helper()
	out, stderr, err := command(client, script, stdin)
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() == 0 || stderr == "" || out != "" {
		t.Fatalf("expected protocol failure, got stdout=%q stderr=%q error=%v", out, stderr, err)
	}
}

func TestEnrollmentAndRestrictedSetupConnection(t *testing.T) {
	h := newHarness(t, nil)
	key := signer(t)
	info, err := h.server.CreateSetup(deviceA, "DESKTOP-SHAREME")
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != 1 || info.Host != "127.0.0.1" || info.Port == 0 ||
		info.Name != "DESKTOP-SHAREME" || !validToken(info.Enrollment) || !strings.HasPrefix(info.Fingerprint, "SHA256:") {
		t.Fatal("invalid setup payload")
	}
	client := dial(t, h, "setup-"+info.Enrollment, key)
	assertExitFailure(t, client, "shareme-v1 request", []byte(`{}`))
	out, _, err := command(client, "shareme-v1 setup", nil)
	if err != nil || out != "{\"status\":\"ready\"}\n" {
		t.Fatalf("setup failed: %q %v", out, err)
	}
	keyID, err := publicKeyID(key.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	h.server.mu.Lock()
	owner := h.server.state.Keys[keyID]
	h.server.mu.Unlock()
	if owner != deviceA {
		t.Fatal("display name changed the enrolled browser authorization owner")
	}
	_ = client.Close()
	reuse, err := tryDial(h, clientConfig(h, "setup-"+info.Enrollment, ssh.PublicKeys(key)))
	if err == nil {
		reuse.Close()
		t.Fatal("enrollment token reused")
	}
	normal := dial(t, h, "shareme", key)
	assertExitFailure(t, normal, "shareme-v1 setup", nil)
	out, _, err = command(normal, "shareme-v1 request", []byte(`{"text":"hello"}`))
	if err != nil || out != "{\"status\":\"ok\"}\n" {
		t.Fatalf("normal request failed: %q %v", out, err)
	}
}

type falseSigner struct {
	public ssh.PublicKey
	wrong  ssh.Signer
	probe  chan struct{}
}

func (s falseSigner) PublicKey() ssh.PublicKey { return s.public }
func (s falseSigner) Sign(random io.Reader, data []byte) (*ssh.Signature, error) {
	select {
	case s.probe <- struct{}{}:
	default:
	}
	if s.wrong == nil {
		return nil, errors.New("unsigned probe only")
	}
	return s.wrong.Sign(random, data)
}

func TestUnsignedProbeAndWrongSignatureDoNotEnroll(t *testing.T) {
	for _, wrongSignature := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsigned", true: "invalid signature"}[wrongSignature], func(t *testing.T) {
			h := newHarness(t, nil)
			key := signer(t)
			info, err := h.server.CreateSetup(deviceA, "Phone")
			if err != nil {
				t.Fatal(err)
			}
			fake := falseSigner{public: key.PublicKey(), probe: make(chan struct{}, 1)}
			if wrongSignature {
				fake.wrong = signer(t)
			}
			client, err := tryDial(h, clientConfig(h, "setup-"+info.Enrollment, ssh.PublicKeys(fake)))
			if err == nil {
				client.Close()
				t.Fatal("client without the private key authenticated")
			}
			select {
			case <-fake.probe:
			default:
				t.Fatal("client did not reach the unsigned public-key probe")
			}
			h.server.mu.Lock()
			_, pending := h.server.tokens[enrollmentID(info.Enrollment)]
			enrolled := len(h.server.state.Keys)
			h.server.mu.Unlock()
			if !pending || enrolled != 0 {
				t.Fatal("unsigned/invalid signature consumed enrollment")
			}
			client = dial(t, h, "setup-"+info.Enrollment, key)
			if _, _, err = command(client, "shareme-v1 setup", nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthenticationRejectsWrongExpiredUnknownAndPassword(t *testing.T) {
	h := newHarness(t, nil)
	key := signer(t)
	info, err := h.server.CreateSetup(deviceA, "Phone")
	if err != nil {
		t.Fatal(err)
	}
	h.server.mu.Lock()
	h.server.tokens[enrollmentID(info.Enrollment)] = enrollment{deviceID: deviceA, expires: time.Now().Add(-time.Second)}
	h.server.mu.Unlock()
	configs := []*ssh.ClientConfig{
		clientConfig(h, "setup-"+info.Enrollment, ssh.PublicKeys(key)),
		clientConfig(h, "setup-"+jobToken, ssh.PublicKeys(key)),
		clientConfig(h, "shareme", ssh.PublicKeys(key)),
		clientConfig(h, "root", ssh.PublicKeys(key)),
		clientConfig(h, "shareme", ssh.Password("password")),
		clientConfig(h, "shareme", ssh.KeyboardInteractive(func(string, string, []string, []bool) ([]string, error) { return nil, nil })),
		clientConfig(h, "shareme"),
	}
	for index, cfg := range configs {
		client, err := tryDial(h, cfg)
		if err == nil {
			client.Close()
			t.Fatalf("invalid authentication %d succeeded", index)
		}
	}
}

func TestConcurrentEnrollmentHasOneWinner(t *testing.T) {
	h := newHarness(t, nil)
	info, err := h.server.CreateSetup(deviceA, "Phone")
	if err != nil {
		t.Fatal(err)
	}
	keys := []ssh.Signer{signer(t), signer(t)}
	var ready sync.WaitGroup
	var successes atomic.Int32
	start := make(chan struct{})
	for _, key := range keys {
		ready.Add(1)
		go func(key ssh.Signer) {
			defer ready.Done()
			<-start
			client, err := tryDial(h, clientConfig(h, "setup-"+info.Enrollment, ssh.PublicKeys(key)))
			if err != nil {
				return
			}
			defer client.Close()
			if _, _, err = command(client, "shareme-v1 setup", nil); err == nil {
				successes.Add(1)
			}
		}(key)
	}
	close(start)
	ready.Wait()
	if successes.Load() != 1 {
		t.Fatalf("got %d setup receipts", successes.Load())
	}
	h.server.mu.Lock()
	defer h.server.mu.Unlock()
	if len(h.server.state.Keys) != 1 || len(h.server.tokens) != 0 {
		t.Fatal("concurrent enrollment corrupted mappings")
	}
}

func TestExistingKeyCannotReassignDevice(t *testing.T) {
	h := newHarness(t, nil)
	key := signer(t)
	setup(t, h, deviceA, key)
	info, err := h.server.CreateSetup(deviceB, "Other phone")
	if err != nil {
		t.Fatal(err)
	}
	client, err := tryDial(h, clientConfig(h, "setup-"+info.Enrollment, ssh.PublicKeys(key)))
	if err == nil {
		client.Close()
		t.Fatal("active key reassigned to another browser device")
	}
	client = dial(t, h, "shareme", key)
	if _, _, err = command(client, "shareme-v1 request", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityPersistenceAndStableFingerprint(t *testing.T) {
	h := newHarness(t, nil)
	key := signer(t)
	info := setup(t, h, deviceA, key)
	pending, err := h.server.CreateSetup(deviceA, "Pending phone")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(h.server.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(info.Enrollment)) || bytes.Contains(data, []byte(`"private"`)) {
		t.Fatal("identity leaked plaintext")
	}
	h.server.Close()
	server, err := newServer(h.server.cfg, h.box)
	if err != nil {
		t.Fatal(err)
	}
	h.server = server
	t.Cleanup(func() { server.Close() })
	if err = server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if server.Status().Fingerprint != info.Fingerprint {
		t.Fatal("SSH host fingerprint changed")
	}
	client := dial(t, h, "shareme", key)
	if _, _, err = command(client, "shareme-v1 request", []byte(`{}`)); err != nil {
		t.Fatal("enrolled key not restored:", err)
	}
	expired, err := tryDial(h, clientConfig(h, "setup-"+pending.Enrollment, ssh.PublicKeys(signer(t))))
	if err == nil {
		expired.Close()
		t.Fatal("enrollment token persisted across restart")
	}
}

func TestProtocolAndBoundedInputs(t *testing.T) {
	h := newHarness(t, nil)
	key := signer(t)
	setup(t, h, deviceA, key)
	client := dial(t, h, "shareme", key)
	suffix := " " + jobID + " " + jobToken
	invalid := []struct {
		script string
		stdin  []byte
	}{
		{"sh -c whoami", nil},
		{"shareme-v1 request; whoami", nil},
		{"shareme-v1 request extra", []byte(`{}`)},
		{"shareme-v1 request", []byte(`{"unfinished":`)},
		{"shareme-v1 request", []byte{'{', '"', 0xff, '"', ':', '1', '}'}},
		{"shareme-v1 request", bytes.Repeat([]byte(" "), maxPreflight+1)},
		{"shareme-v1 text" + suffix, bytes.Repeat([]byte("x"), maxText+1)},
		{"shareme-v1 text" + suffix, []byte{0xff}},
		{"shareme-v1 status" + suffix, []byte{0}},
		{"shareme-v1 cancel" + suffix, []byte("unexpected")},
		{"shareme-v1 status " + strings.ToUpper(jobID) + " " + jobToken, nil},
		{"shareme-v1 status " + jobID + " " + jobToken + "=", nil},
		{"shareme-v1 upload" + suffix + " " + base64.StdEncoding.EncodeToString([]byte("../evil")), nil},
		{"shareme-v1 upload" + suffix + " " + base64.StdEncoding.EncodeToString([]byte(`C:\evil`)), nil},
	}
	for _, tc := range invalid {
		assertExitFailure(t, client, tc.script, tc.stdin)
	}
	for _, operation := range []string{"status", "cancel", "text"} {
		out, stderr, err := command(client, "shareme-v1 "+operation+suffix, nil)
		if err != nil || !json.Valid([]byte(out)) || stderr != "" {
			t.Fatalf("%s failed: %q %q %v", operation, out, stderr, err)
		}
	}
}

func TestNoShellPTYSubsystemOrForwarding(t *testing.T) {
	h := newHarness(t, nil)
	key := signer(t)
	setup(t, h, deviceA, key)
	client := dial(t, h, "shareme", key)
	for _, channelType := range []string{"direct-tcpip", "forwarded-tcpip", "direct-streamlocal@openssh.com", "unknown"} {
		ch, _, err := client.OpenChannel(channelType, nil)
		if err == nil {
			ch.Close()
			t.Fatalf("accepted %s", channelType)
		}
	}
	for _, requestType := range []string{"tcpip-forward", "cancel-tcpip-forward", "keepalive@openssh.com"} {
		ok, _, err := client.SendRequest(requestType, true, nil)
		if err != nil || ok {
			t.Fatalf("global request %s: %v %v", requestType, ok, err)
		}
	}
	for _, requestType := range []string{"pty-req", "shell", "subsystem", "env", "auth-agent-req@openssh.com", "x11-req"} {
		ch, _, err := client.OpenChannel("session", nil)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := ch.SendRequest(requestType, true, nil)
		if err != nil || ok {
			t.Fatalf("session request %s: %v %v", requestType, ok, err)
		}
		ch.Close()
	}
	if ch, _, err := client.OpenChannel("session", []byte("unexpected")); err == nil {
		ch.Close()
		t.Fatal("session accepted extra data")
	}
}
