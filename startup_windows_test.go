package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartupTaskMatchesPackageManifest(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join("packaging", "msix", "AppxManifest.xml.in"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `TaskId="`+startupTaskID+`"`) {
		t.Fatal("the package manifest must declare the startup task the app turns on and off")
	}
	if !strings.Contains(string(manifest), `Enabled="false"`) {
		t.Fatal("the packaged startup task must stay off until the user turns on Start with Windows")
	}
	if runningPackaged() {
		return
	}
	if _, registry := currentStartupStore().(windowsStartupStore); !registry {
		t.Fatal("an unpackaged process must keep using the Run entry")
	}
}

func TestStartupPreferenceFollowsWindows(t *testing.T) {
	for _, scenario := range []struct{ saved, windows bool }{{false, true}, {true, false}, {true, true}, {false, false}} {
		dir := t.TempDir()
		store := &fakeStartupStore{value: startupRegistration{Exists: scenario.windows}}
		current := preferences{IP: "192.168.1.42", DesktopSettings: DesktopSettings{StartWithWindows: scenario.saved, CloseToTray: true}}
		next, err := adoptStartupState(dir, current, store)
		if err != nil || next.StartWithWindows != scenario.windows || next.IP != current.IP || !next.CloseToTray || len(store.writes) != 0 {
			t.Fatalf("%+v: adopted %+v, writes %v, error %v", scenario, next, store.writes, err)
		}
		saved, err := os.ReadFile(filepath.Join(dir, "settings.json"))
		if changed := scenario.saved != scenario.windows; changed != (err == nil) {
			t.Fatalf("%+v: settings must be saved only when the state changed: %q, %v", scenario, saved, err)
		}
		// Saving another option afterwards must leave what Windows has.
		settings := next.DesktopSettings
		settings.CloseToTray = false
		updated, err := commitDesktopSettings(dir, next, settings, store, `C:\ShareMe.exe`)
		if err != nil || store.value.Exists != scenario.windows || updated.StartWithWindows != scenario.windows || updated.CloseToTray {
			t.Fatalf("%+v: unrelated setting changed startup: %+v, %+v, %v", scenario, updated, store.value, err)
		}
	}
	current := preferences{DesktopSettings: DesktopSettings{StartWithWindows: true}}
	if next, err := adoptStartupState(t.TempDir(), current, &fakeStartupStore{readErr: errors.New("unavailable")}); err == nil || next != current {
		t.Fatalf("an unreadable startup state must keep the preference: %+v, %v", next, err)
	}
	dir := t.TempDir()
	// A directory at the destination makes the settings-file replacement fail.
	if err := os.Mkdir(filepath.Join(dir, "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if next, err := adoptStartupState(dir, current, &fakeStartupStore{}); err == nil || next != current {
		t.Fatalf("a failed save must keep the preference: %+v, %v", next, err)
	}
}

// Runs only with package identity, for example through Invoke-CommandInDesktopPackage against the
// registered development package. It restores the startup state it found.
func TestPackagedStartupTaskTurnsOnAndOff(t *testing.T) {
	if !runningPackaged() {
		t.Skip("needs package identity")
	}
	store := currentStartupStore()
	if _, packaged := store.(packagedStartupStore); !packaged {
		t.Fatal("a packaged process must use the Windows startup task")
	}
	before, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Write(before); err != nil {
			t.Errorf("restore startup state: %v", err)
		}
	})
	for _, enabled := range []bool{true, false, true} {
		started := time.Now()
		if err := store.Write(startupRegistration{Exists: enabled}); err != nil {
			t.Fatal(err)
		}
		written := time.Now()
		after, err := store.Read()
		if err != nil || after.Exists != enabled {
			t.Fatalf("startup task enabled=%v after requesting %v: %v", after.Exists, enabled, err)
		}
		t.Logf("enabled=%v: write %s, read %s", enabled, written.Sub(started), time.Since(written))
	}
}

// Runs only with package identity. Windows Settings can switch the startup task behind the app's
// back, so a stale preference must follow Windows at launch and when another option is saved.
func TestPackagedPreferenceFollowsStartupTask(t *testing.T) {
	if !runningPackaged() {
		t.Skip("needs package identity")
	}
	store := packagedStartupStore{}
	before, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Write(before); err != nil {
			t.Errorf("restore startup state: %v", err)
		}
	})
	for _, windows := range []bool{true, false} {
		if err := store.Write(startupRegistration{Exists: windows}); err != nil {
			t.Fatal(err)
		}
		stale := preferences{DesktopSettings: DesktopSettings{StartWithWindows: !windows}}
		dir := t.TempDir()
		synced, err := syncStartup(dir, stale)
		if saved, readErr := readPreferences(dir); err != nil || readErr != nil || synced.StartWithWindows != windows || saved != synced {
			t.Fatalf("launch with Windows startup %v: synced %+v, saved %+v, %v, %v", windows, synced, saved, err, readErr)
		}
		app := &App{dataDir: t.TempDir(), prefsLoaded: true, prefs: stale}
		if err := app.SetDesktopSettings(DesktopSettings{StartWithWindows: !windows, CloseToTray: true}); err != nil {
			t.Fatal(err)
		}
		after, err := store.Read()
		if err != nil || after.Exists != windows || app.prefs.StartWithWindows != windows || !app.prefs.CloseToTray {
			t.Fatalf("another option changed Windows startup %v: task %+v, preference %+v, %v", windows, after, app.prefs.DesktopSettings, err)
		}
		// The startup switch itself still changes Windows.
		if err := app.SetDesktopSettings(DesktopSettings{StartWithWindows: !windows, CloseToTray: true}); err != nil {
			t.Fatal(err)
		}
		if after, err := store.Read(); err != nil || after.Exists == windows || app.prefs.StartWithWindows == windows {
			t.Fatalf("the startup switch did not change Windows startup from %v: task %+v, %v", windows, after, err)
		}
	}
}
