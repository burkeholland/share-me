package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shareme/internal/peer"
	"shareme/internal/transfer"
)

func TestShortcutSetupDoesNotDeadlockReceiverShutdown(t *testing.T) {
	app := &App{}
	app.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := app.createShortcutSetup("0123456789abcdef0123456789abcdef")
		done <- err
	}()
	select {
	case err := <-done:
		app.mu.Unlock()
		if err == nil || !strings.Contains(err.Error(), "busy") {
			t.Fatal("concurrent setup must report a retryable failure", err)
		}
	case <-time.After(time.Second):
		app.mu.Unlock()
		<-done
		t.Fatal("setup waited for the mutex held by receiver shutdown")
	}
}

func shortcutAppFixture(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	engine, err := peer.New(peer.Config{
		DataDir: dir, LocalIP: "127.0.0.1", ServiceURL: "http://127.0.0.1:1", AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	service, err := transfer.New(transfer.Config{DataDir: dir, InboxDir: filepath.Join(dir, "inbox")})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.StartPeer(engine.Listener(), "https://example.test"); err != nil {
		t.Fatal(err)
	}
	app := &App{
		engine: engine, service: service, dataDir: dir, activeIP: "127.0.0.1",
		workCtx: context.Background(), shortcutTest: true, prefsLoaded: true,
		prefs: preferences{Transport: "secure", IP: "127.0.0.1"},
	}
	t.Cleanup(func() {
		app.mu.Lock()
		err := app.stopShortcutLocked()
		app.mu.Unlock()
		if err != nil {
			t.Error(err)
		}
		if err := service.Stop(); err != nil {
			t.Error(err)
		}
	})
	return app
}

func TestShortcutOptInPersistsAndDisablingLeavesBrowserRunning(t *testing.T) {
	app := shortcutAppFixture(t)
	if app.shortcut != nil {
		t.Fatal("SSH receiver started without opt-in")
	}
	if err := app.SetShortcutEnabled(true); err != nil {
		t.Fatal(err)
	}
	first := app.shortcut.Status()
	if !first.Running || first.Fingerprint == "" {
		t.Fatal("opt-in did not start the encrypted receiver")
	}
	saved, err := readPreferences(app.dataDir)
	if err != nil || !saved.ShortcutEnabled || saved.IP != app.activeIP {
		t.Fatal("opt-in was not persisted independently of network settings", err)
	}
	if err := app.SetShortcutEnabled(false); err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", first.Address, 200*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("disabled receiver still accepts connections")
	}
	saved, err = readPreferences(app.dataDir)
	if err != nil || saved.ShortcutEnabled || !app.service.Status().Running {
		t.Fatal("disable did not persist or stopped browser receiving", err)
	}
	if err := app.SetShortcutEnabled(true); err != nil {
		t.Fatal(err)
	}
	if app.shortcut.Status().Fingerprint != first.Fingerprint {
		t.Fatal("re-enabling changed the remembered host identity")
	}
	if err := app.Pause(); err != nil {
		t.Fatal(err)
	}
	if app.shortcut != nil || app.service.Status().Running {
		t.Fatal("pausing left a receiver running")
	}
}

func TestShortcutFailedPreferenceSaveDoesNotLeaveListenerEnabled(t *testing.T) {
	app := shortcutAppFixture(t)
	if err := os.Mkdir(filepath.Join(app.dataDir, "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := app.SetShortcutEnabled(true); err == nil {
		t.Fatal("failed opt-in save was reported as success")
	}
	if app.shortcut != nil || app.prefs.ShortcutEnabled || !app.service.Status().Running {
		t.Fatal("failed save left SSH enabled or stopped browser receiving")
	}
}
