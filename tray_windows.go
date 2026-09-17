package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type TrayCallbacks struct {
	Open  func()
	Hide  func()
	Quit  func()
	Error func(error)
}

// WindowsTray owns a hidden top-level window and its notification icon.
// Callbacks run on separate goroutines and may safely call Stop.
type WindowsTray struct {
	mu        sync.RWMutex
	callbacks TrayCallbacks
	hwnd      windows.Handle
	ready     bool
	stopping  bool
	done      chan struct{}
	stopErr   error

	// The fields below are accessed only on the window's locked OS thread.
	instance       uintptr
	className      *uint16
	classAtom      uintptr
	taskbarCreated uint32
	icon           windows.Handle
	ownedIcon      bool
	iconAdded      bool
	menuActive     bool
	exitRequested  bool
}

const (
	trayWMNull        = 0x0000
	trayWMDestroy     = 0x0002
	trayWMClose       = 0x0010
	trayWMContextMenu = 0x007b
	trayWMCommand     = 0x0111
	trayWMCallback    = 0x8001
	trayWMStop        = 0x8002
	trayWMMenu        = 0x8003
	trayNINSelect     = 0x0400
	trayNINKeySelect  = 0x0401
	trayIconID        = 1
	trayNIMAdd        = 0
	trayNIMDelete     = 2
	trayNIMSetVersion = 4
	trayNIFMessage    = 1
	trayNIFIcon       = 2
	trayNIFTip        = 4
	trayNIFShowTip    = 0x80
	trayVersion       = 4
	trayCmdOpen       = 1
	trayCmdHide       = 2
	trayCmdQuit       = 3
)

type trayPoint struct{ X, Y int32 }

type trayMessage struct {
	HWnd    windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   trayPoint
	Private uint32
}

type trayWindowClass struct {
	Size        uint32
	Style       uint32
	WindowProc  uintptr
	ClassExtra  int32
	WindowExtra int32
	Instance    windows.Handle
	Icon        windows.Handle
	Cursor      windows.Handle
	Background  windows.Handle
	MenuName    *uint16
	ClassName   *uint16
	SmallIcon   windows.Handle
}

type trayNotifyIconData struct {
	Size            uint32
	Window          windows.Handle
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            windows.GUID
	BalloonIcon     windows.Handle
}

var (
	trayUser32   = windows.NewLazySystemDLL("user32.dll")
	trayShell32  = windows.NewLazySystemDLL("shell32.dll")
	trayKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	trayRegisterClass     = trayUser32.NewProc("RegisterClassExW")
	trayUnregisterClass   = trayUser32.NewProc("UnregisterClassW")
	trayCreateWindow      = trayUser32.NewProc("CreateWindowExW")
	trayDestroyWindow     = trayUser32.NewProc("DestroyWindow")
	trayDefWindowProc     = trayUser32.NewProc("DefWindowProcW")
	trayRegisterMessage   = trayUser32.NewProc("RegisterWindowMessageW")
	trayGetMessage        = trayUser32.NewProc("GetMessageW")
	trayTranslateMessage  = trayUser32.NewProc("TranslateMessage")
	trayDispatchMessage   = trayUser32.NewProc("DispatchMessageW")
	trayPostMessage       = trayUser32.NewProc("PostMessageW")
	trayLoadIcon          = trayUser32.NewProc("LoadIconW")
	trayDestroyIcon       = trayUser32.NewProc("DestroyIcon")
	trayCreatePopupMenu   = trayUser32.NewProc("CreatePopupMenu")
	trayAppendMenu        = trayUser32.NewProc("AppendMenuW")
	trayDestroyMenu       = trayUser32.NewProc("DestroyMenu")
	trayTrackPopupMenu    = trayUser32.NewProc("TrackPopupMenu")
	traySetForeground     = trayUser32.NewProc("SetForegroundWindow")
	trayGetCursorPos      = trayUser32.NewProc("GetCursorPos")
	trayEndMenu           = trayUser32.NewProc("EndMenu")
	trayExtractIcon       = trayShell32.NewProc("ExtractIconExW")
	trayShellNotifyIcon   = trayShell32.NewProc("Shell_NotifyIconW")
	trayGetModuleHandle   = trayKernel32.NewProc("GetModuleHandleW")
	traySetLastError      = trayKernel32.NewProc("SetLastError")
	trayWindowProcPointer = windows.NewCallback(trayWindowProc)

	trayControllerMu sync.RWMutex
	trayController   *WindowsTray
)

func StartWindowsTray(callbacks TrayCallbacks) (*WindowsTray, error) {
	w := &WindowsTray{callbacks: callbacks, done: make(chan struct{})}
	trayControllerMu.Lock()
	if trayController != nil {
		trayControllerMu.Unlock()
		return nil, errors.New("Share Me tray is already running")
	}
	trayController = w
	trayControllerMu.Unlock()

	started := make(chan error, 1)
	go w.run(started)
	if err := <-started; err != nil {
		<-w.done
		return nil, errors.Join(err, w.stopErr)
	}
	return w, nil
}

func (w *WindowsTray) Ready() bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.ready && !w.stopping
}

// Stop posts to the owner thread and waits for all native cleanup. If posting
// fails, it returns an error without taking ownership of the window's handles;
// the caller can retry. Concurrent and repeated successful calls share a result.
func (w *WindowsTray) Stop() error {
	if w == nil || w.done == nil {
		return nil
	}
	w.mu.Lock()
	if !w.stopping && w.hwnd != 0 {
		ret, _, err := trayPostMessage.Call(uintptr(w.hwnd), trayWMStop, 0, 0)
		if ret == 0 {
			w.mu.Unlock()
			return trayNativeError("post tray shutdown", err)
		}
		w.stopping = true
		w.ready = false
	}
	w.mu.Unlock()
	<-w.done
	return w.stopErr
}

func (w *WindowsTray) run(started chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() {
		w.stopErr = w.cleanup()
		trayControllerMu.Lock()
		trayController = nil
		trayControllerMu.Unlock()
		close(w.done)
	}()

	if err := w.initialize(); err != nil {
		started <- err
		return
	}
	w.setReady(true)
	started <- nil

	var msg trayMessage
	for !w.exitRequested {
		ret, _, err := trayGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) == -1 {
			w.fail(trayNativeError("read tray window message", err))
			return
		}
		if ret == 0 {
			w.fail(errors.New("tray message loop exited unexpectedly"))
			return
		}
		trayTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		trayDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (w *WindowsTray) initialize() error {
	for _, proc := range []*windows.LazyProc{
		trayRegisterClass, trayUnregisterClass, trayCreateWindow, trayDestroyWindow,
		trayDefWindowProc, trayRegisterMessage, trayGetMessage, trayTranslateMessage,
		trayDispatchMessage, trayPostMessage, trayLoadIcon, trayDestroyIcon,
		trayCreatePopupMenu, trayAppendMenu, trayDestroyMenu, trayTrackPopupMenu,
		traySetForeground, trayGetCursorPos, trayEndMenu, trayExtractIcon,
		trayShellNotifyIcon, trayGetModuleHandle, traySetLastError,
	} {
		if err := proc.Find(); err != nil {
			return fmt.Errorf("load Windows tray API: %w", err)
		}
	}
	messageName := windows.StringToUTF16Ptr("TaskbarCreated")
	messageID, _, err := trayRegisterMessage.Call(uintptr(unsafe.Pointer(messageName)))
	if messageID == 0 {
		return trayNativeError("register TaskbarCreated message", err)
	}
	w.taskbarCreated = uint32(messageID)
	w.instance, _, err = trayGetModuleHandle.Call(0)
	if w.instance == 0 {
		return trayNativeError("get tray module handle", err)
	}
	w.className = windows.StringToUTF16Ptr("ShareMeTrayWindow")
	class := trayWindowClass{
		Size:       uint32(unsafe.Sizeof(trayWindowClass{})),
		WindowProc: trayWindowProcPointer,
		Instance:   windows.Handle(w.instance),
		ClassName:  w.className,
	}
	w.classAtom, _, err = trayRegisterClass.Call(uintptr(unsafe.Pointer(&class)))
	if w.classAtom == 0 {
		return trayNativeError("register Share Me tray window class", err)
	}
	// Parent = 0 makes this an invisible top-level window. HWND_MESSAGE would
	// prevent Explorer's TaskbarCreated broadcast from reaching it.
	hwnd, _, err := trayCreateWindow.Call(
		0, uintptr(unsafe.Pointer(w.className)), uintptr(unsafe.Pointer(w.className)),
		0, 0, 0, 0, 0, 0, 0, w.instance, 0,
	)
	if hwnd == 0 {
		return trayNativeError("create Share Me tray window", err)
	}
	w.mu.Lock()
	w.hwnd = windows.Handle(hwnd)
	w.mu.Unlock()

	path, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable for tray icon: %w", err)
	}
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode tray icon path: %w", err)
	}
	count, _, _ := trayExtractIcon.Call(
		uintptr(unsafe.Pointer(pathUTF16)), 0, 0, uintptr(unsafe.Pointer(&w.icon)), 1,
	)
	w.ownedIcon = w.icon != 0
	if uint32(count) == ^uint32(0) {
		return errors.New("extract executable tray icon failed")
	}
	if w.icon == 0 {
		icon, _, err := trayLoadIcon.Call(0, 32512) // Shared IDI_APPLICATION, including go test executables.
		if icon == 0 {
			return trayNativeError("load fallback tray icon", err)
		}
		w.icon = windows.Handle(icon)
	}
	return w.addIcon()
}

func (w *WindowsTray) iconData() trayNotifyIconData {
	w.mu.RLock()
	hwnd := w.hwnd
	w.mu.RUnlock()
	return trayNotifyIconData{
		Size: uint32(unsafe.Sizeof(trayNotifyIconData{})), Window: hwnd, ID: trayIconID,
	}
}

func (w *WindowsTray) addIcon() error {
	data := w.iconData()
	data.Flags = trayNIFMessage | trayNIFIcon | trayNIFTip | trayNIFShowTip
	data.CallbackMessage = trayWMCallback
	data.Icon = w.icon
	copy(data.Tip[:], windows.StringToUTF16("Share Me"))
	if ret, _, _ := trayShellNotifyIcon.Call(trayNIMAdd, uintptr(unsafe.Pointer(&data))); ret == 0 {
		return errors.New("add Share Me notification icon failed")
	}
	w.iconAdded = true
	data.Version = trayVersion
	if ret, _, _ := trayShellNotifyIcon.Call(trayNIMSetVersion, uintptr(unsafe.Pointer(&data))); ret == 0 {
		return errors.Join(errors.New("set Share Me notification icon version failed"), w.deleteIcon())
	}
	return nil
}

func (w *WindowsTray) deleteIcon() error {
	if !w.iconAdded {
		return nil
	}
	data := w.iconData()
	if ret, _, _ := trayShellNotifyIcon.Call(trayNIMDelete, uintptr(unsafe.Pointer(&data))); ret == 0 {
		return errors.New("delete Share Me notification icon failed")
	}
	w.iconAdded = false
	return nil
}

func (w *WindowsTray) cleanup() error {
	w.mu.Lock()
	w.stopping = true
	w.ready = false
	w.mu.Unlock()
	var errs []error
	errs = append(errs, w.deleteIcon())
	w.mu.Lock()
	hwnd := w.hwnd
	w.hwnd = 0
	w.mu.Unlock()
	if hwnd != 0 {
		if ret, _, err := trayDestroyWindow.Call(uintptr(hwnd)); ret == 0 {
			errs = append(errs, trayNativeError("destroy tray window", err))
		}
	}
	if w.ownedIcon {
		if ret, _, err := trayDestroyIcon.Call(uintptr(w.icon)); ret == 0 {
			errs = append(errs, trayNativeError("destroy executable tray icon", err))
		}
		w.ownedIcon = false
	}
	if w.classAtom != 0 {
		if ret, _, err := trayUnregisterClass.Call(uintptr(unsafe.Pointer(w.className)), w.instance); ret == 0 {
			errs = append(errs, trayNativeError("unregister tray window class", err))
		}
	}
	return errors.Join(errs...)
}

func (w *WindowsTray) setReady(ready bool) {
	w.mu.Lock()
	w.ready = ready && !w.stopping
	w.mu.Unlock()
}

func (w *WindowsTray) report(err error) {
	if err != nil && w.callbacks.Error != nil {
		go w.callbacks.Error(err)
	}
}

func (w *WindowsTray) fail(err error) {
	w.setReady(false)
	w.report(err)
}

func (w *WindowsTray) dispatch(command uint32) {
	if !w.Ready() {
		return
	}
	var callback func()
	switch command {
	case trayCmdOpen:
		callback = w.callbacks.Open
	case trayCmdHide:
		callback = w.callbacks.Hide
	case trayCmdQuit:
		callback = w.callbacks.Quit
	}
	if callback != nil {
		go func() {
			if w.Ready() {
				callback()
			}
		}()
	}
}

func trayWindowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	trayControllerMu.RLock()
	w := trayController
	trayControllerMu.RUnlock()
	if w != nil {
		w.mu.RLock()
		matches := uintptr(w.hwnd) == hwnd
		stopping := w.stopping
		w.mu.RUnlock()
		if matches {
			switch {
			case msg == trayWMStop || msg == trayWMClose:
				w.setReady(false)
				w.exitRequested = true
				if msg == trayWMClose && !stopping {
					w.report(errors.New("tray window was closed unexpectedly"))
				}
				if w.menuActive {
					if ret, _, err := trayEndMenu.Call(); ret == 0 {
						w.report(trayNativeError("end tray menu during shutdown", err))
					}
				}
				return 0
			case msg == trayWMDestroy:
				w.exitRequested = true
				w.fail(errors.New("tray window was destroyed unexpectedly"))
				return 0
			case msg == w.taskbarCreated:
				if !stopping {
					w.setReady(false)
					w.iconAdded = false // Explorer discarded the previous registration.
					if err := w.addIcon(); err != nil {
						w.fail(fmt.Errorf("restore tray after Explorer restart: %w", err))
					} else {
						w.setReady(true)
					}
				}
				return 0
			case msg == trayWMCallback:
				if !stopping {
					event, id := trayDecodeEvent(lParam)
					if id == trayIconID {
						switch event {
						case trayNINSelect, trayNINKeySelect:
							w.dispatch(trayCmdOpen)
						case trayWMContextMenu:
							if ret, _, err := trayPostMessage.Call(hwnd, trayWMMenu, wParam, 0); ret == 0 {
								w.report(trayNativeError("post tray context menu", err))
							}
						}
					}
				}
				return 0
			case msg == trayWMCommand:
				if !stopping && lParam == 0 && wParam>>16 == 0 {
					w.dispatch(uint32(wParam))
				}
				return 0
			case msg == trayWMMenu:
				if !stopping {
					w.showMenu(hwnd, trayDecodePoint(wParam))
				}
				return 0
			}
		}
	}
	ret, _, _ := trayDefWindowProc.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func trayDecodeEvent(lParam uintptr) (event, id uint16) {
	// NOTIFYICON_VERSION_4 packs the event and icon ID into lParam, not wParam.
	return uint16(lParam), uint16(lParam >> 16)
}

func trayDecodePoint(wParam uintptr) trayPoint {
	return trayPoint{X: int32(int16(wParam)), Y: int32(int16(wParam >> 16))}
}

func (w *WindowsTray) showMenu(hwnd uintptr, point trayPoint) {
	if w.menuActive || !w.Ready() {
		return
	}
	w.menuActive = true
	defer func() { w.menuActive = false }()
	menu, _, err := trayCreatePopupMenu.Call()
	if menu == 0 {
		w.report(trayNativeError("create tray menu", err))
		return
	}
	defer func() {
		if ret, _, err := trayDestroyMenu.Call(menu); ret == 0 {
			w.report(trayNativeError("destroy tray menu", err))
		}
	}()
	for _, item := range []struct {
		id    uintptr
		label string
	}{
		{trayCmdOpen, "Open Share Me"},
		{trayCmdHide, "Minimize to tray"},
		{0, ""},
		{trayCmdQuit, "Quit"},
	} {
		flags := uintptr(0)
		var label *uint16
		if item.id == 0 {
			flags = 0x800 // MF_SEPARATOR
		} else {
			label = windows.StringToUTF16Ptr(item.label)
		}
		if ret, _, err := trayAppendMenu.Call(menu, flags, item.id, uintptr(unsafe.Pointer(label))); ret == 0 {
			w.report(trayNativeError("append tray menu item", err))
			return
		}
	}
	if point.X == -1 && point.Y == -1 {
		if ret, _, err := trayGetCursorPos.Call(uintptr(unsafe.Pointer(&point))); ret == 0 {
			w.report(trayNativeError("locate tray menu", err))
			return
		}
	}
	if ret, _, _ := traySetForeground.Call(hwnd); ret == 0 {
		w.report(errors.New("foreground tray menu owner failed"))
	}
	defer func() {
		if ret, _, err := trayPostMessage.Call(hwnd, trayWMNull, 0, 0); ret == 0 {
			w.report(trayNativeError("finish tray menu dismissal", err))
		}
	}()
	traySetLastError.Call(0)
	// TPM_RETURNCMD | TPM_NONOTIFY | TPM_RIGHTBUTTON. The nested native menu
	// loop remains on the owner thread and can receive our shutdown message.
	command, _, err := trayTrackPopupMenu.Call(
		menu, 0x100|0x80|0x2, uintptr(point.X), uintptr(point.Y), 0, hwnd, 0,
	)
	if command == 0 {
		if err != nil && !errors.Is(err, windows.ERROR_SUCCESS) {
			w.report(trayNativeError("track tray menu", err))
		}
		return
	}
	w.dispatch(uint32(command))
}

func trayNativeError(operation string, err error) error {
	if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
		return fmt.Errorf("%s failed", operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
