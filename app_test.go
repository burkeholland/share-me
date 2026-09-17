package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"shareme/internal/transfer"
)

func TestDesktopPermanentLinkAndPause(t *testing.T) {
	dir := t.TempDir()
	service, err := transfer.New(transfer.Config{
		DataDir: dir, InboxDir: filepath.Join(dir, "inbox"),
		Assets:        fstest.MapFS{"phone.html": {Data: []byte("phone")}},
		AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Stop() })
	app := &App{service: service, dataDir: dir}
	state, err := app.GetState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Status.Running || !strings.HasPrefix(state.QR, "data:image/png;base64,") {
		t.Fatal("running receiver must supply a QR image")
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(state.QR, "data:image/png;base64,"))
	if err != nil || len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatal("phone link QR must be a valid PNG")
	}
	next, err := app.GetState()
	if err != nil {
		t.Fatal(err)
	}
	if next.QR != state.QR || next.Status.Address != state.Status.Address {
		t.Fatal("the phone link must stay the same without codes or expiration")
	}
	if err := app.Pause(); err != nil {
		t.Fatal(err)
	}
	paused, err := app.GetState()
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status.Running || paused.QR != "" {
		t.Fatal("paused receiver must not advertise a QR code")
	}
	if err := app.Decide("not-a-request", true); err == nil {
		t.Fatal("an unknown request cannot be approved")
	}
}

func TestCorruptPreferencesRemainVisible(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("USERPROFILE", dir)
	if err := os.MkdirAll(filepath.Join(dir, "ShareMe"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ShareMe", "settings.json")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	app := &App{assets: fstest.MapFS{"phone.html": {Data: []byte("phone")}}}
	err := app.initialize()
	if err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("corrupt preferences must surface an error, got %v", err)
	}
	if app.service != nil && app.service.Status().Running {
		t.Fatal("invalid settings must not start a receiver")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{broken" {
		t.Fatal("corrupt user settings must not be silently replaced")
	}
}

func TestHeadlessRequiresExplicitScope(t *testing.T) {
	if err := runHeadless(fstest.MapFS{}, nil); err == nil {
		t.Fatal("headless mode must require an explicit address and data directory")
	}
}
