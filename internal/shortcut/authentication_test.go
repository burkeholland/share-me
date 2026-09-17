package shortcut

import (
	"crypto/rand"
	"crypto/rsa"
	"io"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type pausedSigner struct {
	ssh.Signer
	entered chan struct{}
	resume  chan struct{}
}

func (s pausedSigner) Sign(random io.Reader, data []byte) (*ssh.Signature, error) {
	close(s.entered)
	<-s.resume
	return s.Signer.Sign(random, data)
}

func TestEnrollmentRecheckedAfterSignature(t *testing.T) {
	for _, action := range []string{"expire", "deny", "revoke"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t, nil)
			info, err := h.server.CreateSetup(deviceA, "Phone")
			if err != nil {
				t.Fatal(err)
			}
			key := pausedSigner{Signer: signer(t), entered: make(chan struct{}), resume: make(chan struct{})}
			result := make(chan bool, 1)
			go func() {
				client, err := tryDial(h, clientConfig(h, "setup-"+info.Enrollment, ssh.PublicKeys(key)))
				if err != nil {
					result <- false
					return
				}
				defer client.Close()
				_, _, err = command(client, "shareme-v1 setup", nil)
				result <- err == nil
			}()
			mustFinish(t, key.entered)
			switch action {
			case "expire":
				h.server.mu.Lock()
				h.server.tokens[enrollmentID(info.Enrollment)] = enrollment{deviceID: deviceA, expires: time.Now().Add(-time.Second)}
				h.server.mu.Unlock()
			case "deny":
				h.deny(deviceA)
			case "revoke":
				if err = h.server.RevokeDevice(deviceA); err != nil {
					t.Fatal(err)
				}
			}
			close(key.resume)
			select {
			case success := <-result:
				if success {
					t.Fatal("stale public-key probe was sufficient to enroll")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("post-probe handshake did not finish")
			}
			h.server.mu.Lock()
			enrolled := len(h.server.state.Keys)
			h.server.mu.Unlock()
			if enrolled != 0 {
				t.Fatal("invalidated enrollment persisted a key")
			}
		})
	}
}

func TestModernRSAWorksButSHA1AndLegacyKEXDoNot(t *testing.T) {
	h := newHarness(t, nil)
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	modern, err := ssh.NewSignerWithAlgorithms(key.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSASHA256})
	if err != nil {
		t.Fatal(err)
	}
	setup(t, h, deviceA, modern)
	client := dial(t, h, "shareme", modern)
	if _, _, err = command(client, "shareme-v1 request", []byte(`{}`)); err != nil {
		t.Fatal("RSA SHA-256 rejected:", err)
	}
	client.Close()
	legacy, err := ssh.NewSignerWithAlgorithms(key.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSA})
	if err != nil {
		t.Fatal(err)
	}
	if client, err = tryDial(h, clientConfig(h, "shareme", ssh.PublicKeys(legacy))); err == nil {
		client.Close()
		t.Fatal("legacy RSA SHA-1 signature accepted")
	}
	cfg := clientConfig(h, "shareme", ssh.PublicKeys(modern))
	cfg.KeyExchanges = []string{"diffie-hellman-group14-sha1"}
	if client, err = tryDial(h, cfg); err == nil {
		client.Close()
		t.Fatal("legacy SHA-1 key exchange accepted")
	}
}

func TestSessionExecPayloadAndSecondExecAreRestricted(t *testing.T) {
	h := newHarness(t, nil)
	key := signer(t)
	setup(t, h, deviceA, key)
	client := dial(t, h, "shareme", key)
	for _, payload := range [][]byte{
		{0, 0, 1},
		append(ssh.Marshal(struct{ Command string }{"shareme-v1 request"}), 0),
	} {
		ch, requests, err := client.OpenChannel("session", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ch.SendRequest("exec", true, payload); err != nil {
			t.Fatal(err)
		}
		var code uint32
		for request := range requests {
			if request.Type == "exit-status" {
				var status struct{ Status uint32 }
				if err = ssh.Unmarshal(request.Payload, &status); err != nil {
					t.Fatal(err)
				}
				code = status.Status
			}
		}
		ch.Close()
		if code == 0 {
			t.Fatal("malformed exec payload did not fail")
		}
	}
	ch, _, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	payload := ssh.Marshal(struct{ Command string }{"shareme-v1 request"})
	ok, err := ch.SendRequest("exec", true, payload)
	if err != nil || !ok {
		t.Fatalf("first exec failed: %v %v", ok, err)
	}
	ok, err = ch.SendRequest("exec", true, payload)
	if err != nil || ok {
		t.Fatalf("second exec not rejected: %v %v", ok, err)
	}
	ch.CloseWrite()
}
