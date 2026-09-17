package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeStartupStore struct {
	value   startupRegistration
	writes  []startupRegistration
	readErr error
	failAt  int
}

func (s *fakeStartupStore) Read() (startupRegistration, error) { return s.value, s.readErr }
func (s *fakeStartupStore) Write(value startupRegistration) error {
	s.writes = append(s.writes, value)
	if s.failAt == len(s.writes) {
		return errors.New("registry write refused")
	}
	s.value = value
	return nil
}

func TestPreferencesDefaultsAndMigration(t *testing.T) {
	dir := t.TempDir()
	pref, err := readPreferences(dir)
	if err != nil || pref.StartWithWindows || pref.StartMinimized || !pref.MinimizeToTray || pref.ShortcutEnabled {
		t.Fatalf("unexpected defaults: %+v, %v", pref, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"ip":"192.168.1.42"}`), 0600); err != nil {
		t.Fatal(err)
	}
	pref, err = readPreferences(dir)
	if err != nil || pref.IP != "192.168.1.42" || !pref.MinimizeToTray || pref.StartWithWindows {
		t.Fatalf("existing network must survive migration: %+v, %v", pref, err)
	}
}

func TestDesktopSettingsPreserveNetworkAndPersist(t *testing.T) {
	dir := t.TempDir()
	store := &fakeStartupStore{}
	current := preferences{IP: "192.168.1.42", ShortcutEnabled: true}
	settings := DesktopSettings{StartWithWindows: true, StartMinimized: true, MinimizeToTray: true}
	updated, err := commitDesktopSettings(dir, current, settings, store, `C:\Share Me\ShareMe.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if updated.IP != current.IP || !updated.ShortcutEnabled || updated.DesktopSettings != settings ||
		store.value.Command != `"C:\Share Me\ShareMe.exe" --startup` {
		t.Fatalf("settings or startup command incorrect: %+v, %+v", updated, store.value)
	}
	saved, err := readPreferences(dir)
	if err != nil || saved != updated {
		t.Fatalf("settings must survive restart: %+v, %v", saved, err)
	}
	updated.IP = "192.168.1.99"
	if err := savePreferences(dir, updated); err != nil {
		t.Fatal(err)
	}
	saved, err = readPreferences(dir)
	if err != nil || saved.DesktopSettings != settings {
		t.Fatal("changing network must not reset desktop settings")
	}
	disabled := DesktopSettings{}
	updated, err = commitDesktopSettings(dir, saved, disabled, store, `C:\Share Me\ShareMe.exe`)
	if err != nil || store.value.Exists || updated.DesktopSettings != disabled {
		t.Fatalf("disabling must remove startup and preserve explicit false settings: %v", err)
	}
	reopened, err := readPreferences(dir)
	if err != nil || reopened.MinimizeToTray {
		t.Fatal("explicitly disabling minimize to tray must survive restart")
	}
}

func TestSettingsFailureRestoresExactStartupEntry(t *testing.T) {
	dir := t.TempDir()
	// A directory at the destination makes the final settings-file replacement fail.
	if err := os.Mkdir(filepath.Join(dir, "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	before := startupRegistration{Exists: true, Command: `%LOCALAPPDATA%\OldShareMe.exe`, Expand: true}
	store := &fakeStartupStore{value: before}
	current := preferences{IP: "192.168.1.5"}
	updated, err := commitDesktopSettings(dir, current, DesktopSettings{StartWithWindows: true}, store, `C:\New\ShareMe.exe`)
	if err == nil || updated != current || store.value != before || len(store.writes) != 2 {
		t.Fatalf("failed persistence must restore original entry: %+v, %+v, %v", updated, store, err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, "settings-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatal("failed save must not leave temporary files")
	}
	store = &fakeStartupStore{value: before, failAt: 2}
	_, err = commitDesktopSettings(dir, current, DesktopSettings{}, store, `C:\New\ShareMe.exe`)
	if err == nil || !strings.Contains(err.Error(), "restore Windows startup") {
		t.Fatalf("rollback failures must be reported: %v", err)
	}
}

func TestRegistryFailureDoesNotSavePreferences(t *testing.T) {
	for _, store := range []*fakeStartupStore{{readErr: errors.New("registry unavailable")}, {failAt: 1}} {
		dir := t.TempDir()
		_, err := commitDesktopSettings(dir, preferences{}, DesktopSettings{StartWithWindows: true}, store, `C:\ShareMe.exe`)
		if err == nil {
			t.Fatal("registry failures must be reported")
		}
		if _, err := os.Stat(filepath.Join(dir, "settings.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed registry operation must not save success-shaped preferences")
		}
	}
}

func TestStartupCommandRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{"", "ShareMe.exe", "C:\\ShareMe.exe\" --other", "C:\\bad\n.exe", "C:\\bad\x00.exe"} {
		if _, err := startupCommand(path); err == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
}
