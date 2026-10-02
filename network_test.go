package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shareme/internal/outbox"
	"shareme/internal/transfer"
)

func TestSelectNetworkPreservesSelectionAndRecoversStaleIP(t *testing.T) {
	networks := []transfer.Network{{Name: "Wi-Fi", IP: "192.168.1.10"}, {Name: "Ethernet", IP: "10.0.0.10"}}
	for _, requested := range []string{"", "192.168.2.10", "10.0.0.10"} {
		want := networks[0].IP
		if requested == networks[1].IP {
			want = requested
		}
		if got, err := selectNetworkIP(requested, networks); err != nil || got != want {
			t.Fatalf("requested=%q selected=%q error=%v", requested, got, err)
		}
	}
	if ip, err := selectNetworkIP("192.168.1.10", nil); ip != "" || err == nil {
		t.Fatal("offline selection must not start a receiver")
	}
}

func TestStartPersistsFallbackAndNetworkNoticeClears(t *testing.T) {
	networks, err := transfer.Networks()
	if err != nil {
		t.Fatal(err)
	}
	if len(networks) == 0 {
		t.Skip("requires a current private network")
	}
	dir := t.TempDir()
	service, err := transfer.New(transfer.Config{DataDir: dir, InboxDir: filepath.Join(dir, "inbox")})
	if err != nil {
		t.Fatal(err)
	}
	box, err := outbox.New(filepath.Join(dir, "outbox"), 128)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{service: service, outbox: box, dataDir: dir, prefsLoaded: true,
		prefs: preferences{Transport: "secure", ServiceURL: "https://127.0.0.1:1"}}
	t.Cleanup(func() {
		if err := app.Pause(); err != nil {
			t.Error(err)
		}
	})
	if err := app.Start("192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	saved, err := readPreferences(dir)
	if err != nil || saved.IP != networks[0].IP || app.activeIP != saved.IP {
		t.Fatalf("did not persist the actual bound IP: %+v error=%v", saved, err)
	}
	app.activeIP = "192.0.2.1"
	app.settingsErr = "settings operation failed"
	state, err := app.GetState()
	if err != nil || !strings.Contains(state.Error, "network address changed") || !strings.Contains(state.Error, app.settingsErr) {
		t.Fatalf("notice hid an operational error: %q error=%v", state.Error, err)
	}
	app.activeIP = saved.IP
	state, err = app.GetState()
	if err != nil || state.Error != app.settingsErr {
		t.Fatalf("network notice did not clear: %q error=%v", state.Error, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineNetworkInstructionNamesResume(t *testing.T) {
	ip, err := selectNetworkIP("", nil)
	if ip != "" || err == nil || !strings.Contains(err.Error(), "then click Resume") || strings.Contains(err.Error(), "Refresh") {
		t.Fatalf("offline instruction=%v selected=%q", err, ip)
	}
}
