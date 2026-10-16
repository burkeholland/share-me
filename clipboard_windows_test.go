//go:build windows

package main

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"testing"
)

func TestWindowsClipboardImageIntegration(t *testing.T) {
	if os.Getenv("SHAREME_CLIPBOARD_TEST") != "1" {
		t.Skip("set SHAREME_CLIPBOARD_TEST=1 after copying an image")
	}
	payload, err := readClipboardPayload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if payload.Name != "Clipboard image.png" || payload.MIME != "image/png" || len(payload.Data) == 0 {
		t.Fatalf("unexpected clipboard image payload: name=%q MIME=%q bytes=%d", payload.Name, payload.MIME, len(payload.Data))
	}
	if _, err := png.Decode(bytes.NewReader(payload.Data)); err != nil {
		t.Fatalf("clipboard image is not a valid PNG: %v", err)
	}
}
