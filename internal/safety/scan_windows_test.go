package safety

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanOrderingAndFailure(t *testing.T) {
	for _, scenario := range []struct {
		name, content                string
		locateErr, runErr, markErr   error
		wantRun, wantMark, wantError bool
	}{
		{name: "clean", content: "A harmless note.", wantRun: true, wantMark: true},
		{name: "renamed executable", content: "MZ not a real program", wantError: true},
		{name: "renamed script", content: "#! /bin/sh\n", wantError: true},
		{name: "scanner missing", content: "note", locateErr: errors.New("missing"), wantError: true},
		{name: "scanner blocked", content: "note", runErr: errors.New("exit 2"), wantRun: true, wantError: true},
		{name: "attachment tagging failed", content: "note", markErr: errors.New("not NTFS"), wantRun: true, wantMark: true, wantError: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "note.txt")
			if err := os.WriteFile(path, []byte(scenario.content), 0600); err != nil {
				t.Fatal(err)
			}
			ran, marked := false, false
			err := scan(context.Background(), path,
				func() (string, error) { return "scanner", scenario.locateErr },
				func(context.Context, string, string) error { ran = true; return scenario.runErr },
				func(string) error { marked = true; return scenario.markErr })
			if (err != nil) != scenario.wantError || ran != scenario.wantRun || marked != scenario.wantMark {
				t.Fatalf("error=%v scanned=%v marked=%v", err, ran, marked)
			}
		})
	}
}

func TestAttachmentMarkerSurvivesRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "temporary.txt")
	if err := os.WriteFile(path, []byte("A harmless note."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := markAttachment(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "received.txt")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(target + ":Zone.Identifier")
	if err != nil || !strings.Contains(string(marker), "ZoneId=3") {
		t.Fatalf("attachment marker did not survive rename: %v", err)
	}
}

func TestDefenderScanBenignFile(t *testing.T) {
	if os.Getenv("SHAREME_SCAN_TEST") != "1" {
		t.Skip("set SHAREME_SCAN_TEST=1 to exercise the installed Windows Defender")
	}
	path := filepath.Join(t.TempDir(), "share-me-scan-check.txt")
	if err := os.WriteFile(path, []byte("Share Me antivirus integration check. This is a harmless text file."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Scan(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}
