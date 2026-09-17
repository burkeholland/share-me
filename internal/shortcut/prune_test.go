package shortcut

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestDisabledRevocationsPruneDurablyAndReclaimFullQuota(t *testing.T) {
	h := newHarness(t, nil)
	keyA, keyB := signer(t), signer(t)
	setup(t, h, deviceA, keyA)
	setup(t, h, deviceB, keyB)
	previous := h.server
	fingerprint := previous.Status().Fingerprint
	previous.Close()

	state := previous.state.copy()
	forgotten := []string{deviceA}
	for len(state.Keys) < maxKeys {
		key, err := publicKeyID(signer(t).PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		device := fmt.Sprintf("%032x", len(state.Keys))
		state.Keys[key] = device
		h.mu.Lock()
		h.allowed[device] = true
		h.mu.Unlock()
		forgotten = append(forgotten, device)
	}
	if err := previous.store.save(state); err != nil {
		t.Fatal(err)
	}
	for _, device := range forgotten {
		// No live receiver exists to receive RevokeDevice while disabled.
		h.deny(device)
	}
	server, err := newServer(previous.cfg, h.box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	h.server = server
	if server.Status().Running || server.Status().Fingerprint != fingerprint {
		t.Fatal("loading bound a listener or changed the host identity")
	}
	retainedKey, err := publicKeyID(keyB.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	durable, _, err := server.store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(server.state.Keys) != 1 || len(durable.Keys) != 1 || durable.Keys[retainedKey] != deviceB {
		t.Fatal("startup did not durably prune only the forgotten browsers")
	}
	if err = server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	clientB := dial(t, h, "shareme", keyB)
	if _, _, err = command(clientB, "shareme-v1 request", []byte(`{}`)); err != nil {
		t.Fatal("remaining paired browser lost SSH access:", err)
	}
	clientA, err := tryDial(h, clientConfig(h, "shareme", ssh.PublicKeys(keyA)))
	if err == nil {
		clientA.Close()
		t.Fatal("forgotten browser key authenticated")
	}
	setup(t, h, deviceB, signer(t))
	durable, _, err = server.store.load()
	if err != nil || len(durable.Keys) != 2 {
		t.Fatal("full orphaned-key quota was not reclaimed for a new enrollment")
	}

	server.Close()
	before, err := os.ReadFile(server.store.path)
	if err != nil {
		t.Fatal(err)
	}
	h.box.fail.Store(true)
	defer h.box.fail.Store(false)
	reloaded, err := newServer(server.cfg, h.box)
	if err != nil {
		t.Fatal("unchanged allowed mappings should not require a write:", err)
	}
	reloaded.Close()
	after, err := os.ReadFile(server.store.path)
	if err != nil || !bytes.Equal(before, after) || reloaded.Status().Fingerprint != fingerprint {
		t.Fatal("subsequent load rewrote unchanged state or changed the host identity")
	}
}

func TestStartupPruneFailuresDoNotEnableOrOverwriteState(t *testing.T) {
	for _, failure := range []string{"persistence", "authorization panic"} {
		t.Run(failure, func(t *testing.T) {
			h := newHarness(t, nil)
			setup(t, h, deviceA, signer(t))
			setup(t, h, deviceB, signer(t))
			h.server.Close()
			h.deny(deviceA)
			before, err := os.ReadFile(h.server.store.path)
			if err != nil {
				t.Fatal(err)
			}
			cfg := h.server.cfg
			switch failure {
			case "persistence":
				h.box.fail.Store(true)
			case "authorization panic":
				cfg.IsDeviceAllowed = func(device string) bool {
					if device == deviceB {
						panic("private callback details")
					}
					return h.allow(device)
				}
			}
			rejected, err := newServer(cfg, h.box)
			if rejected != nil {
				rejected.Close()
				t.Fatal("startup returned a receiver despite failed pruning")
			}
			if err == nil || strings.Contains(err.Error(), "private callback details") {
				t.Fatal("startup failure missing or leaked callback details")
			}
			after, err := os.ReadFile(h.server.store.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed pruning damaged the original encrypted state")
			}
			h.box.fail.Store(false)
			retry, err := newServer(h.server.cfg, h.box)
			if err != nil {
				t.Fatal("pruning could not be retried:", err)
			}
			retry.Close()
			durable, _, err := retry.store.load()
			if err != nil || len(durable.Keys) != 1 {
				t.Fatal("successful retry did not durably remove forgotten mappings")
			}
		})
	}
}
