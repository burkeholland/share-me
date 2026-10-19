//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	clipboardDIB         = 8
	clipboardUnicodeText = 13
	clipboardFiles       = 15
	clipboardDIBV5       = 17
)

var (
	user32                     = windows.NewLazySystemDLL("user32.dll")
	kernel32                   = windows.NewLazySystemDLL("kernel32.dll")
	shell32                    = windows.NewLazySystemDLL("shell32.dll")
	openClipboard              = user32.NewProc("OpenClipboard")
	closeClipboard             = user32.NewProc("CloseClipboard")
	isClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	getClipboardData           = user32.NewProc("GetClipboardData")
	registerClipboardFormat    = user32.NewProc("RegisterClipboardFormatW")
	globalLock                 = kernel32.NewProc("GlobalLock")
	globalUnlock               = kernel32.NewProc("GlobalUnlock")
	globalSize                 = kernel32.NewProc("GlobalSize")
	dragQueryFile              = shell32.NewProc("DragQueryFileW")
)

func readClipboardPayload(context.Context) (clipboardPayload, error) {
	opened := false
	for range 6 {
		result, _, _ := openClipboard.Call(0)
		if result != 0 {
			opened = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !opened {
		return clipboardPayload{}, errors.New("Windows could not open the clipboard because another app is using it. Try again")
	}
	defer closeClipboard.Call()

	if formatAvailable(clipboardFiles) {
		files, err := clipboardFilePaths()
		if err != nil {
			return clipboardPayload{}, fmt.Errorf("read copied files: %w", err)
		}
		if len(files) > 0 {
			return clipboardPayload{Files: files}, nil
		}
	}
	pngName, err := windows.UTF16PtrFromString("PNG")
	if err != nil {
		return clipboardPayload{}, err
	}
	pngFormat, _, callErr := registerClipboardFormat.Call(uintptr(unsafe.Pointer(pngName)))
	if pngFormat == 0 {
		return clipboardPayload{}, fmt.Errorf("register PNG clipboard format: %w", callErr)
	}
	if formatAvailable(uint32(pngFormat)) {
		data, err := clipboardGlobalBytes(uint32(pngFormat))
		if err != nil {
			return clipboardPayload{}, fmt.Errorf("read clipboard PNG: %w", err)
		}
		return clipboardPayload{Name: "Clipboard image.png", MIME: "image/png", Data: data}, nil
	}
	for _, format := range []uint32{clipboardDIBV5, clipboardDIB} {
		if !formatAvailable(format) {
			continue
		}
		data, err := clipboardGlobalBytes(format)
		if err != nil {
			return clipboardPayload{}, fmt.Errorf("read clipboard image: %w", err)
		}
		encoded, err := encodeDIBAsPNG(data)
		if err != nil {
			return clipboardPayload{}, err
		}
		return clipboardPayload{Name: "Clipboard image.png", MIME: "image/png", Data: encoded}, nil
	}
	if formatAvailable(clipboardUnicodeText) {
		text, err := clipboardText()
		if err != nil {
			return clipboardPayload{}, fmt.Errorf("read clipboard text: %w", err)
		}
		if strings.TrimSpace(text) != "" {
			return clipboardPayload{Text: text}, nil
		}
	}
	return clipboardPayload{}, errors.New("the clipboard does not contain text, an image, or copied files that Share Me can send")
}

func formatAvailable(format uint32) bool {
	result, _, _ := isClipboardFormatAvailable.Call(uintptr(format))
	return result != 0
}

func clipboardHandle(format uint32) (uintptr, error) {
	handle, _, err := getClipboardData.Call(uintptr(format))
	if handle == 0 {
		if err == syscall.Errno(0) {
			err = errors.New("clipboard data is unavailable")
		}
		return 0, err
	}
	return handle, nil
}

func clipboardGlobalBytes(format uint32) ([]byte, error) {
	handle, err := clipboardHandle(format)
	if err != nil {
		return nil, err
	}
	size, _, sizeErr := globalSize.Call(handle)
	if size == 0 {
		return nil, fmt.Errorf("clipboard data has no contents: %w", sizeErr)
	}
	pointer, _, lockErr := globalLock.Call(handle)
	if pointer == 0 {
		return nil, fmt.Errorf("lock clipboard data: %w", lockErr)
	}
	defer globalUnlock.Call(handle)
	data := make([]byte, size)
	var read uintptr
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), pointer, &data[0], size, &read); err != nil {
		return nil, fmt.Errorf("copy clipboard data: %w", err)
	}
	if read != size {
		return nil, errors.New("the clipboard data changed while it was being read")
	}
	return data, nil
}

func clipboardText() (string, error) {
	data, err := clipboardGlobalBytes(clipboardUnicodeText)
	if err != nil {
		return "", err
	}
	if len(data)%2 != 0 {
		return "", errors.New("clipboard text has an invalid length")
	}
	words := make([]uint16, len(data)/2)
	for index := range words {
		words[index] = uint16(data[index*2]) | uint16(data[index*2+1])<<8
	}
	return windows.UTF16ToString(words), nil
}

func clipboardFilePaths() ([]string, error) {
	handle, err := clipboardHandle(clipboardFiles)
	if err != nil {
		return nil, err
	}
	count, _, queryErr := dragQueryFile.Call(handle, uintptr(0xFFFFFFFF), 0, 0)
	if count == 0 {
		if queryErr != syscall.Errno(0) {
			return nil, queryErr
		}
		return nil, nil
	}
	if count > 20 {
		return nil, errors.New("the clipboard contains more than 20 files; copy fewer files and try again")
	}
	files := make([]string, 0, count)
	for index := uintptr(0); index < count; index++ {
		length, _, err := dragQueryFile.Call(handle, index, 0, 0)
		if length == 0 {
			return nil, fmt.Errorf("read copied file name: %w", err)
		}
		buffer := make([]uint16, length+1)
		written, _, err := dragQueryFile.Call(handle, index, uintptr(unsafe.Pointer(&buffer[0])), length+1)
		if written != length {
			return nil, fmt.Errorf("read copied file name: %w", err)
		}
		files = append(files, windows.UTF16ToString(buffer))
	}
	return files, nil
}
