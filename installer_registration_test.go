package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestShortcutRegistrationRequiresPublisherConfirmation(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("scripts", "register-shortcut.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name                     string
		signed, confirm, badHash bool
	}{
		{name: "iCloud needs confirmation"},
		{name: "iCloud confirmed", confirm: true},
		{name: "signed file needs confirmation", signed: true},
		{name: "signed file confirmed", signed: true, confirm: true},
		{name: "changed signed file rejected", signed: true, confirm: true, badHash: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "scripts", "register-shortcut.ps1")
			if err := os.WriteFile(path, script, 0600); err != nil {
				t.Fatal(err)
			}
			resultPath := filepath.Join(dir, "shortcut-release.json")
			args := []string{"-NoProfile", "-NonInteractive", "-File", path}
			if scenario.signed {
				args = append(args, "-SignedArtifact")
				if err := os.Mkdir(filepath.Join(dir, "shortcuts"), 0700); err != nil {
					t.Fatal(err)
				}
				fixture := []byte("AEA1unsigned test fixture")
				if err := os.WriteFile(filepath.Join(dir, "shortcuts", "Share Me.shortcut"), fixture, 0600); err != nil {
					t.Fatal(err)
				}
				hash := fmt.Sprintf("%x", sha256.Sum256(fixture))
				if scenario.badHash {
					hash = "wrong"
				}
				resultPath = filepath.Join(dir, "shortcuts", "ShareMe.release.json")
				metadata, err := json.Marshal(map[string]any{
					"schemaVersion": 2, "transport": "ssh-v1", "sha256": hash, "physicalIPhoneVerified": false,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(resultPath, metadata, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				args = append(args, "-ICloudURL", "https://www.icloud.com/shortcuts/00000000000000000000000000000000")
			}
			if scenario.confirm {
				args = append(args, "-VerifiedOnIPhone")
			}
			output, commandErr := exec.Command("pwsh", args...).CombinedOutput()
			success := scenario.confirm && !scenario.badHash
			if (commandErr == nil) != success {
				t.Fatalf("unexpected registration result: %v: %s", commandErr, output)
			}
			data, err := os.ReadFile(resultPath)
			if !scenario.signed && !success {
				if !os.IsNotExist(err) {
					t.Fatal("unconfirmed link was registered", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				Verified bool `json:"physicalIPhoneVerified"`
			}
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record.Verified != success {
				t.Fatal("publisher confirmation was invented or lost")
			}
		})
	}
}
