//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func readClipboardPayload(ctx context.Context) (clipboardPayload, error) {
	text, err := wailsruntime.ClipboardGetText(ctx)
	if err != nil {
		return clipboardPayload{}, fmt.Errorf("read clipboard text: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return clipboardPayload{}, errors.New("this platform only exposes clipboard text to Share Me, and the clipboard contains no text")
	}
	return clipboardPayload{Text: text}, nil
}
