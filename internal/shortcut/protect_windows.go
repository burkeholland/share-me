//go:build windows

package shortcut

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type platformProtector struct{}

func (platformProtector) protect(data []byte) ([]byte, error)   { return dpapi(data, true) }
func (platformProtector) unprotect(data []byte) ([]byte, error) { return dpapi(data, false) }

func dpapi(data []byte, encrypt bool) ([]byte, error) {
	if len(data) == 0 || len(data) > maxSealedState {
		return nil, errors.New("invalid shortcut DPAPI input")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	runtime.KeepAlive(data)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Data == nil || out.Size == 0 || out.Size > maxSealedState {
		return nil, errors.New("invalid shortcut DPAPI output")
	}
	source := unsafe.Slice(out.Data, int(out.Size))
	result := append([]byte(nil), source...)
	clear(source)
	return result, nil
}

func replaceIdentity(from, to string) error {
	f, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(f, t, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
