package shortcut

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func uploadCommand(name string) string {
	return "shareme-v1 upload " + jobID + " " + jobToken + " " + base64.StdEncoding.EncodeToString([]byte(name))
}

func TestRawBinaryStreamingEOFAndReceiptAfterSave(t *testing.T) {
	entered := make(chan struct{})
	approve := make(chan struct{})
	atSave := make(chan struct{})
	saveDone := make(chan struct{})
	received := make(chan []byte, 1)
	h := newHarness(t, func(ctx context.Context, request Request) (Response, error) {
		if request.Operation != "upload" || request.Name != "NUL bytes.bin" || request.ID != jobID ||
			request.Token != jobToken || request.DeviceID != deviceA || !strings.HasPrefix(request.RemoteAddr, "127.0.0.1:") {
			return Response{}, errors.New("wrong request metadata")
		}
		if _, ok := request.Body.(*streamBody); !ok {
			return Response{}, errors.New("upload is not streamed")
		}
		close(entered)
		select {
		case <-approve:
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
		hash := sha256.New()
		n, err := io.CopyBuffer(hash, request.Body, make([]byte, 4096))
		if err != nil {
			return Response{}, err
		}
		if n != 4096*257 {
			return Response{}, errors.New("raw binary size changed")
		}
		received <- hash.Sum(nil)
		close(atSave)
		select {
		case <-saveDone:
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
		return Response{StatusCode: 201, Body: []byte(`{"status":"saved"}`)}, nil
	})
	key := signer(t)
	setup(t, h, deviceA, key)
	client := dial(t, h, "shareme", key)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	input, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Start(uploadCommand("NUL bytes.bin")); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, entered)
	// Handler approval is reached with no stdin bytes, ruling out eager upload
	// reads. The only read-ahead available to an unapproved sender is SSH's
	// fixed receive window, not a whole-file allocation in this package.
	close(approve)
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	expected := sha256.New()
	for i := 0; i < 257; i++ {
		expected.Write(chunk)
		if _, err = input.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	result := make(chan error, 1)
	go func() { result <- session.Wait() }()
	select {
	case err := <-result:
		t.Fatalf("receipt before EOF: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err = input.Close(); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, atSave)
	select {
	case err := <-result:
		t.Fatalf("receipt before save/scanning completed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(saveDone)
	select {
	case err = <-result:
		if err != nil || stdout.String() != "{\"status\":\"saved\"}\n" || stderr.Len() != 0 {
			t.Fatalf("receipt: %q %q %v", stdout.String(), stderr.String(), err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("receipt missing after EOF and save")
	}
	if !bytes.Equal(<-received, expected.Sum(nil)) {
		t.Fatal("binary data (including NUL) changed in transit")
	}
}

func TestUploadLimitAndUnconsumedSuccess(t *testing.T) {
	for _, mode := range []string{"oversized", "unconsumed", "swallowed read error", "exact limit"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, func(_ context.Context, request Request) (Response, error) {
				var err error
				if mode != "unconsumed" {
					_, err = io.Copy(io.Discard, request.Body)
				}
				if mode == "swallowed read error" {
					err = nil
				}
				return Response{StatusCode: 201, Body: []byte(`{"status":"saved"}`)}, err
			}, func(s *Server) { s.cfg.MaxFileBytes = 32 })
			key := signer(t)
			setup(t, h, deviceA, key)
			client := dial(t, h, "shareme", key)
			data := bytes.Repeat([]byte{0, 255}, 17)
			if mode == "exact limit" {
				out, _, err := command(client, uploadCommand("binary.bin"), data[:32])
				if err != nil || out != "{\"status\":\"saved\"}\n" {
					t.Fatalf("exact limit failed: %q %v", out, err)
				}
			} else {
				assertExitFailure(t, client, uploadCommand("binary.bin"), data)
			}
		})
	}
}

func TestApplicationRejectionDoesNotWaitForUpload(t *testing.T) {
	h := newHarness(t, func(_ context.Context, request Request) (Response, error) {
		return Response{StatusCode: 403, Body: []byte(`{"error":"approval denied"}`)}, nil
	})
	key := signer(t)
	setup(t, h, deviceA, key)
	client := dial(t, h, "shareme", key)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	// Keep stdin open with no bytes. A denied upload must still respond.
	if _, err = session.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	out, err := session.Output(uploadCommand("file.bin"))
	if err != nil || string(out) != "{\"error\":\"approval denied\"}\n" {
		t.Fatalf("application error should exit zero: %q %v", out, err)
	}
}

func TestCloseCancelsBlockedInputAndHandler(t *testing.T) {
	for _, mode := range []string{"read", "callback"} {
		t.Run(mode, func(t *testing.T) {
			entered, exited := make(chan struct{}), make(chan struct{})
			h := newHarness(t, func(ctx context.Context, request Request) (Response, error) {
				close(entered)
				defer close(exited)
				if mode == "read" {
					_, err := io.Copy(io.Discard, request.Body)
					return Response{}, err
				}
				<-ctx.Done()
				return Response{}, ctx.Err()
			})
			key := signer(t)
			setup(t, h, deviceA, key)
			client := dial(t, h, "shareme", key)
			session, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if _, err = session.StdinPipe(); err != nil {
				t.Fatal(err)
			}
			if err = session.Start(uploadCommand("blocked.bin")); err != nil {
				t.Fatal(err)
			}
			mustFinish(t, entered)
			closed := make(chan struct{})
			go func() { h.server.Close(); close(closed) }()
			mustFinish(t, closed)
			mustFinish(t, exited)
			if h.server.Status().Running {
				t.Fatal("Close did not change status")
			}
		})
	}
}

func TestRevokeCancelsParentAndPreservesOtherDevice(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	h := newHarness(t, func(ctx context.Context, request Request) (Response, error) {
		if request.DeviceID == deviceA {
			close(entered)
			defer close(exited)
			<-ctx.Done()
			return Response{}, ctx.Err()
		}
		return Response{StatusCode: 200, Body: []byte(`{"status":"ok"}`)}, nil
	})
	keyA, keyB := signer(t), signer(t)
	setup(t, h, deviceA, keyA)
	setup(t, h, deviceB, keyB)
	pending, err := h.server.CreateSetup(deviceA, "Pending")
	if err != nil {
		t.Fatal(err)
	}
	clientA := dial(t, h, "shareme", keyA)
	clientB := dial(t, h, "shareme", keyB)
	session, err := clientA.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err = session.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err = session.Start(uploadCommand("blocked.bin")); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, entered)
	h.deny(deviceA)
	if err = h.server.RevokeDevice(deviceA); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, exited)
	if _, _, err = command(clientB, "shareme-v1 status "+jobID+" "+jobToken, nil); err != nil {
		t.Fatal("another device was disconnected:", err)
	}
	for _, user := range []string{"shareme", "setup-" + pending.Enrollment} {
		client, err := tryDial(h, clientConfig(h, user, ssh.PublicKeys(keyA)))
		if err == nil {
			client.Close()
			t.Fatal("revoked parent authenticated")
		}
	}
}

func TestAllowedParentCheckedOnEveryCommandAndHandshake(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, func(context.Context, Request) (Response, error) {
		calls.Add(1)
		return Response{StatusCode: 200, Body: []byte(`{}`)}, nil
	})
	key := signer(t)
	setup(t, h, deviceA, key)
	client := dial(t, h, "shareme", key)
	// A completed command also establishes that post-handshake claim finished.
	if _, _, err := command(client, "shareme-v1 request", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	h.deny(deviceA)
	assertExitFailure(t, client, "shareme-v1 request", []byte(`{}`))
	if calls.Load() != 1 {
		t.Fatal("revoked command reached handler")
	}
	other, err := tryDial(h, clientConfig(h, "shareme", ssh.PublicKeys(key)))
	if err == nil {
		other.Close()
		t.Fatal("revoked device authenticated without explicit disk revocation")
	}
}

func TestAbruptDisconnectCancelsHandler(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	h := newHarness(t, func(ctx context.Context, request Request) (Response, error) {
		close(entered)
		defer close(exited)
		_, err := io.Copy(io.Discard, request.Body)
		return Response{}, err
	})
	key := signer(t)
	setup(t, h, deviceA, key)
	client := dial(t, h, "shareme", key)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err = session.Start(uploadCommand("partial.bin")); err != nil {
		t.Fatal(err)
	}
	mustFinish(t, entered)
	client.Close()
	mustFinish(t, exited)
	waitFor(t, func() bool {
		h.server.mu.Lock()
		defer h.server.mu.Unlock()
		return h.server.channelCount == 0 && len(h.server.connections) == 0
	})
}

func TestConcurrentConnectionAndChannelLimits(t *testing.T) {
	t.Run("connections and close of pending handshakes", func(t *testing.T) {
		h := newHarness(t, nil, func(s *Server) { s.limits.connections = 2 })
		var clients []net.Conn
		for i := 0; i < 2; i++ {
			c, err := net.Dial("tcp4", h.server.Status().Address)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			clients = append(clients, c)
		}
		waitFor(t, func() bool {
			h.server.mu.Lock()
			defer h.server.mu.Unlock()
			return len(h.server.connections) == 2
		})
		third, err := net.Dial("tcp4", h.server.Status().Address)
		if err != nil {
			t.Fatal(err)
		}
		defer third.Close()
		third.SetReadDeadline(time.Now().Add(time.Second))
		var data [1]byte
		if _, err = third.Read(data[:]); err == nil {
			t.Fatal("excess connection received a server handshake")
		}
		done := make(chan struct{})
		go func() { h.server.Close(); close(done) }()
		mustFinish(t, done)
	})
	t.Run("per connection and total channels", func(t *testing.T) {
		h := newHarness(t, nil, func(s *Server) { s.limits.channels, s.limits.perConnection = 2, 1 })
		key := signer(t)
		setup(t, h, deviceA, key)
		clients := []*ssh.Client{dial(t, h, "shareme", key), dial(t, h, "shareme", key), dial(t, h, "shareme", key)}
		first, err := clients[0].NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		if extra, err := clients[0].NewSession(); err == nil {
			extra.Close()
			t.Fatal("per-connection channel limit bypassed")
		}
		second, err := clients[1].NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer second.Close()
		if extra, err := clients[2].NewSession(); err == nil {
			extra.Close()
			t.Fatal("total channel limit bypassed")
		}
	})
}

func TestDeadlinesCancelRequestsAndHandshake(t *testing.T) {
	t.Run("handshake", func(t *testing.T) {
		h := newHarness(t, nil, func(s *Server) { s.limits.handshake = 100 * time.Millisecond })
		raw, err := net.Dial("tcp4", h.server.Status().Address)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		raw.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = io.Copy(io.Discard, raw)
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatal("server did not expire an incomplete handshake")
		}
	})
	t.Run("request", func(t *testing.T) {
		entered, exited := make(chan struct{}), make(chan struct{})
		h := newHarness(t, func(ctx context.Context, request Request) (Response, error) {
			close(entered)
			defer close(exited)
			<-ctx.Done()
			return Response{}, ctx.Err()
		}, func(s *Server) { s.limits.request = 300 * time.Millisecond })
		key := signer(t)
		setup(t, h, deviceA, key)
		client := dial(t, h, "shareme", key)
		result := make(chan struct{})
		go func() { command(client, "shareme-v1 request", []byte(`{}`)); close(result) }()
		mustFinish(t, entered)
		mustFinish(t, exited)
		mustFinish(t, result)
	})
}
