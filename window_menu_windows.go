package main

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowClassName = "ShareMeWindow"
	wmSysCommand    = 0x0112
	scKeyMenu       = 0xf100
)

var windowFindWindowEx = trayUser32.NewProc("FindWindowExW")

// ShowWindowMenu opens the window menu (Restore, Move, Size, Minimize, Maximize, Close) after a
// right-click on the page's title bar. It posts the command Alt+Space sends, so Windows shows its
// own menu on the window's thread and handles the choice as usual: Close still asks beforeClose
// and Minimize still follows the tray option. A popup menu can only be tracked by the thread
// that owns the window, which Wails does not expose, so the menu opens where Alt+Space puts it.
func (a *App) ShowWindowMenu() {
	class, err := windows.UTF16PtrFromString(windowClassName)
	if err != nil {
		return
	}
	var hwnd uintptr
	for {
		hwnd, _, _ = windowFindWindowEx.Call(0, hwnd, uintptr(unsafe.Pointer(class)), 0)
		if hwnd == 0 {
			return
		}
		var pid uint32
		windows.GetWindowThreadProcessId(windows.HWND(hwnd), &pid)
		if pid == uint32(os.Getpid()) {
			break
		}
	}
	trayPostMessage.Call(hwnd, wmSysCommand, scKeyMenu, ' ')
}
