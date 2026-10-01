package main

import (
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
