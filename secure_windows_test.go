//go:build windows

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"shareme/internal/outbox"
	"shareme/internal/peer"
)

func TestRevokeLastPhoneWhilePaused(t *testing.T) {
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	plain, err := json.Marshal(map[string]any{
		"version": 1, "private": private,
		"devices": map[string]any{id: map[string]any{
			"id": id, "name": "Phone", "secret": make([]byte, 32), "createdAt": time.Now().UTC(),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var protected windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &protected); err != nil {
		t.Fatal(err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(protected.Data)))
	if err := os.WriteFile(filepath.Join(dir, "peer-identity.dpapi"), unsafe.Slice(protected.Data, int(protected.Size)), 0600); err != nil {
		t.Fatal(err)
	}
	engine, err := peer.New(peer.Config{DataDir: dir, ServiceURL: "http://127.0.0.1:1", LocalIP: "127.0.0.1", AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	box, err := outbox.New(filepath.Join(dir, "outbox"), 128)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.AddText(context.Background(), id, "queued text"); err != nil {
		t.Fatal(err)
	}
	app := &App{engine: engine, outbox: box}
	if err := app.RevokePhone(id); err != nil {
		t.Fatal(err)
	}
	if len(engine.Devices()) != 0 || len(box.List(id)) != 0 || app.pairURL != "" {
		t.Fatal("paused revocation retained credentials/offers or created an invitation")
	}
}
