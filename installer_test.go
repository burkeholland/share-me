package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerRequiresPublishedArtifact(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("scripts", "stage-shortcut.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, artifact, link string
		metadata, wantError  string
		legacy               bool
		unverified           bool
		wantFailure          bool
		wantState            string
	}{
		{name: "not published", wantState: "unpublished"},
		{name: "unsigned rejected", artifact: "<?xml version=\"1.0\"?><plist/>", wantFailure: true, wantError: "not an Apple-signed archive"},
		{name: "external link rejected", link: "https://example.com/installer", wantFailure: true},
		{name: "legacy link rejected", link: "https://www.icloud.com/shortcuts/00000000000000000000000000000000", legacy: true, wantFailure: true, wantError: "Legacy HTTP"},
		{name: "Apple link shape accepted", link: "https://www.icloud.com/shortcuts/00000000000000000000000000000000", wantState: "published"},
		{name: "unverified Apple link remains unpublished", link: "https://www.icloud.com/shortcuts/00000000000000000000000000000000", unverified: true, wantState: "unpublished"},
		{name: "archive missing metadata", artifact: "AEA1test", wantFailure: true, wantError: "metadata is missing"},
		{name: "legacy archive metadata rejected", artifact: "AEA1test", metadata: `{"schemaVersion":1,"transport":"http"}`, wantFailure: true, wantError: "does not match"},
		{name: "archive hash mismatch", artifact: "AEA1test", metadata: `{"schemaVersion":2,"transport":"ssh-v1","sha256":"wrong"}`, wantFailure: true, wantError: "does not match"},
		{name: "matching archive metadata staged", artifact: "AEA1test",
			metadata: fmt.Sprintf(`{"schemaVersion":2,"transport":"ssh-v1","sha256":"%x","physicalIPhoneVerified":true}`, sha256.Sum256([]byte("AEA1test"))), wantState: "published"},
		{name: "signed but unverified remains unpublished", artifact: "AEA1test",
			metadata: fmt.Sprintf(`{"schemaVersion":2,"transport":"ssh-v1","sha256":"%x","physicalIPhoneVerified":false}`, sha256.Sum256([]byte("AEA1test"))), wantState: "unpublished"},
		{name: "missing verification remains unpublished", artifact: "AEA1test",
			metadata: fmt.Sprintf(`{"schemaVersion":2,"transport":"ssh-v1","sha256":"%x"}`, sha256.Sum256([]byte("AEA1test"))), wantState: "unpublished"},
		{name: "string verification is not boolean confirmation", artifact: "AEA1test",
			metadata: fmt.Sprintf(`{"schemaVersion":2,"transport":"ssh-v1","sha256":"%x","physicalIPhoneVerified":"true"}`, sha256.Sum256([]byte("AEA1test"))), wantState: "unpublished"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "scripts", "stage-shortcut.ps1")
			if err := os.WriteFile(path, script, 0600); err != nil {
				t.Fatal(err)
			}
			if scenario.artifact != "" {
				if err := os.MkdirAll(filepath.Join(dir, "shortcuts"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "shortcuts", "Share Me.shortcut"), []byte(scenario.artifact), 0600); err != nil {
					t.Fatal(err)
				}
				if scenario.metadata != "" {
					if err := os.WriteFile(filepath.Join(dir, "shortcuts", "ShareMe.release.json"), []byte(scenario.metadata), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario.link != "" {
				release := map[string]any{"icloudURL": scenario.link, "schemaVersion": 2, "transport": "ssh-v1", "physicalIPhoneVerified": !scenario.unverified}
				if scenario.legacy {
					release["schemaVersion"], release["transport"] = 1, "http"
				}
				config, err := json.Marshal(release)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "shortcut-release.json"), config, 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-File", path)
			output, err := command.CombinedOutput()
			if scenario.wantFailure {
				if err == nil {
					t.Fatal("invalid installer input was accepted")
				}
				if scenario.wantError != "" && !strings.Contains(string(output), scenario.wantError) {
					t.Fatalf("unexpected rejection: %s", output)
				}
				return
			}
			if err != nil {
				t.Fatalf("stage installer: %v: %s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(dir, "frontend", "public", "assets", "shortcut-install.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest struct {
				SchemaVersion int    `json:"schemaVersion"`
				Transport     string `json:"transport"`
				State         string `json:"state"`
				URL           string `json:"url"`
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.SchemaVersion != 2 || manifest.Transport != "ssh-v1" {
				t.Fatal("installer must identify the encrypted SSH template")
			}
			if manifest.State != scenario.wantState {
				t.Fatalf("unexpected state: %s", data)
			}
			if manifest.State == "unpublished" && manifest.URL != "" {
				t.Fatal("unpublished installer must not advertise a URL")
			}
			if scenario.artifact != "" && manifest.State == "published" {
				staged, err := os.ReadFile(filepath.Join(dir, "frontend", "public", "assets", "ShareMe.shortcut"))
				if err != nil || string(staged) != scenario.artifact {
					t.Fatal("staging changed the signed archive bytes", err)
				}
			}
			if manifest.State == "unpublished" {
				_, err := os.Stat(filepath.Join(dir, "frontend", "public", "assets", "ShareMe.shortcut"))
				if !os.IsNotExist(err) {
					t.Fatal("unverified archive was distributed", err)
				}
			}
		})
	}
}
