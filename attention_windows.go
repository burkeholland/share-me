package main

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

func flashTransferWindow() error {
	user32 := windows.NewLazySystemDLL("user32.dll")
	class, err := windows.UTF16PtrFromString("ShareMeWindow")
	if err != nil {
		return err
	}
	handle, _, _ := user32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(class)), 0)
	if handle == 0 {
		return errors.New("Share Me window is not available for taskbar attention")
	}
	info := struct {
		Size    uint32
		Window  windows.Handle
		Flags   uint32
		Count   uint32
		Timeout uint32
	}{Window: windows.Handle(handle), Flags: 2, Count: 3}
	info.Size = uint32(unsafe.Sizeof(info))
	// FlashWindowEx returns the previous activation state, not a success flag.
	user32.NewProc("FlashWindowEx").Call(uintptr(unsafe.Pointer(&info)))
	return nil
}
