package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTrayNativeLayouts(t *testing.T) {
	pointerSize := unsafe.Sizeof(uintptr(0))
	var message trayMessage
	var class trayWindowClass
	var icon trayNotifyIconData
	expected := []uintptr{48, 80, 976, 8, 32, 40, 816, 968}
	if pointerSize == 4 {
		expected = []uintptr{32, 48, 956, 4, 20, 24, 800, 952}
	}
	actual := []uintptr{
		unsafe.Sizeof(message), unsafe.Sizeof(class), unsafe.Sizeof(icon),
		unsafe.Offsetof(icon.Window), unsafe.Offsetof(icon.Icon), unsafe.Offsetof(icon.Tip),
		unsafe.Offsetof(icon.Version), unsafe.Offsetof(icon.BalloonIcon),
	}
	for i := range expected {
		if actual[i] != expected[i] {
			t.Errorf("native layout %d = %d, want %d (%d-bit)", i, actual[i], expected[i], pointerSize*8)
		}
	}
}

func TestTrayVersionFourEvents(t *testing.T) {
	for _, event := range []uint16{trayNINSelect, trayNINKeySelect, trayWMContextMenu} {
		gotEvent, gotID := trayDecodeEvent(uintptr(trayIconID<<16) | uintptr(event))
		if gotEvent != event || gotID != trayIconID {
			t.Fatalf("decoded event=%#x id=%d, want %#x and %d", gotEvent, gotID, event, trayIconID)
		}
	}
	_, legacyID := trayDecodeEvent(0x0202) // Legacy WM_LBUTTONUP is not a v4 selection.
	if legacyID == trayIconID {
		t.Fatal("legacy event incorrectly accepted as a v4 icon event")
	}
}

func TestTraySignedCoordinates(t *testing.T) {
	for _, point := range []trayPoint{{0, 0}, {-1, -1}, {-1920, 1080}, {32767, -32768}} {
		packed := uintptr(uint16(point.X)) | uintptr(uint16(point.Y))<<16
		if actual := trayDecodePoint(packed); actual != point {
			t.Errorf("decoded %v, want %v", actual, point)
		}
	}
}

func TestTrayNativeErrors(t *testing.T) {
	for _, err := range []error{nil, windows.ERROR_SUCCESS, windows.ERROR_ACCESS_DENIED} {
		result := trayNativeError("operation", err)
		if result == nil || !strings.Contains(result.Error(), "operation") {
			t.Fatalf("missing operation context: %v", result)
		}
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(result, err) {
			t.Fatalf("native error not preserved: %v", result)
		}
	}
}

func TestTrayCallbacksAreReentrant(t *testing.T) {
	callbacks := make(chan uint32, 3)
	tray := &WindowsTray{ready: true, done: make(chan struct{})}
	tray.callbacks = TrayCallbacks{
		Open: func() { tray.Ready(); callbacks <- trayCmdOpen },
		Hide: func() { tray.Ready(); callbacks <- trayCmdHide },
		Quit: func() { tray.Ready(); callbacks <- trayCmdQuit },
	}
	for _, command := range []uint32{trayCmdOpen, trayCmdHide, trayCmdQuit} {
		tray.dispatch(command)
		select {
		case got := <-callbacks:
			if got != command {
				t.Fatalf("callback=%d, want %d", got, command)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("callback blocked")
		}
	}
	tray.setReady(false)
	tray.dispatch(trayCmdOpen)
	select {
	case <-callbacks:
		t.Fatal("callback dispatched when not ready")
	default:
	}
}

func TestTrayErrorCallbackOutsideMutex(t *testing.T) {
	tray := &WindowsTray{ready: true}
	reported := make(chan bool, 1)
	tray.callbacks.Error = func(error) { reported <- tray.Ready() }
	tray.fail(errors.New("test failure"))
	select {
	case ready := <-reported:
		if ready {
			t.Fatal("failure callback observed ready tray")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failure callback deadlocked")
	}
}

func TestTrayNilLifecycle(t *testing.T) {
	for _, tray := range []*WindowsTray{nil, {}} {
		if tray.Ready() {
			t.Fatal("uninitialized tray is ready")
		}
		if err := tray.Stop(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrayNativeLifecycle(t *testing.T) {
	if os.Getenv("SHAREME_TRAY_TEST") != "1" {
		t.Skip("set SHAREME_TRAY_TEST=1 to create and destroy this test's notification icon (requires Explorer)")
	}
	for i := 0; i < 3; i++ {
		nativeErrors := make(chan error, 16)
		tray, err := StartWindowsTray(TrayCallbacks{Error: func(err error) { nativeErrors <- err }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := tray.Stop(); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		})
		if !tray.Ready() {
			t.Fatal("tray not ready after successful native registration")
		}
		tray.mu.RLock()
		hwnd := tray.hwnd
		tray.mu.RUnlock()
		if hwnd == 0 {
			t.Fatal("tray has no owner window")
		}
		parent, _, _ := trayUser32.NewProc("GetParent").Call(uintptr(hwnd))
		if parent != 0 {
			t.Fatal("tray window is not top-level")
		}
		if second, err := StartWindowsTray(TrayCallbacks{}); err == nil {
			second.Stop()
			t.Fatal("concurrent tray controller unexpectedly started")
		}
		var wait sync.WaitGroup
		results := make(chan error, 8)
		for j := 0; j < cap(results); j++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				results <- tray.Stop()
			}()
		}
		wait.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Error(err)
			}
		}
		if tray.Ready() {
			t.Fatal("tray remained ready after Stop")
		}
		if alive, _, _ := trayUser32.NewProc("IsWindow").Call(uintptr(hwnd)); alive != 0 {
			t.Fatal("Stop returned before the owner window was destroyed")
		}
		if err := tray.Stop(); err != nil {
			t.Fatal("repeated Stop:", err)
		}
		select {
		case err := <-nativeErrors:
			t.Fatal("unexpected asynchronous native error:", err)
		default:
		}
	}
}

func TestTrayNativeSelectionCanStop(t *testing.T) {
	if os.Getenv("SHAREME_TRAY_TEST") != "1" {
		t.Skip("set SHAREME_TRAY_TEST=1 to test a native notification callback (requires Explorer)")
	}
	started := make(chan *WindowsTray, 1)
	stopped := make(chan error, 1)
	tray, err := StartWindowsTray(TrayCallbacks{
		Open: func() { stopped <- (<-started).Stop() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tray.Stop() })
	started <- tray
	tray.mu.RLock()
	hwnd := tray.hwnd
	tray.mu.RUnlock()
	if ret, _, err := trayPostMessage.Call(uintptr(hwnd), trayWMCallback, 0, trayIconID<<16|trayNINKeySelect); ret == 0 {
		t.Fatal(trayNativeError("post test selection to own tray", err))
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("callback could not stop tray; likely running inside WndProc")
	}
	if tray.Ready() {
		t.Fatal("stopped callback left tray ready")
	}
}

type trayTestRect struct{ Left, Top, Right, Bottom int32 }

type trayTestIconIdentifier struct {
	Size   uint32
	Window windows.Handle
	ID     uint32
	GUID   windows.GUID
}

func trayTestIconRect(hwnd windows.Handle) (trayTestRect, error) {
	identifier := trayTestIconIdentifier{
		Size: uint32(unsafe.Sizeof(trayTestIconIdentifier{})), Window: hwnd, ID: trayIconID,
	}
	var rect trayTestRect
	result, _, _ := trayShell32.NewProc("Shell_NotifyIconGetRect").Call(
		uintptr(unsafe.Pointer(&identifier)), uintptr(unsafe.Pointer(&rect)),
	)
	if int32(result) < 0 {
		return rect, fmt.Errorf("Shell_NotifyIconGetRect: HRESULT %#x", uint32(result))
	}
	return rect, nil
}

func trayTestPost(t *testing.T, hwnd windows.Handle, message uint32, wParam, lParam uintptr) {
	t.Helper()
	if ret, _, err := trayPostMessage.Call(uintptr(hwnd), uintptr(message), wParam, lParam); ret == 0 {
		t.Fatal(trayNativeError("post message to own test tray", err))
	}
}

func trayTestWait(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func TestTrayNativeIconRecovery(t *testing.T) {
	if os.Getenv("SHAREME_TRAY_TEST") != "1" {
		t.Skip("set SHAREME_TRAY_TEST=1 to remove and restore only this test's icon (requires Explorer)")
	}
	nativeErrors := make(chan error, 16)
	tray, err := StartWindowsTray(TrayCallbacks{Error: func(err error) { nativeErrors <- err }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tray.Stop(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	data := tray.iconData()
	if _, err := trayTestIconRect(data.Window); err != nil {
		t.Fatal("initial native icon is missing:", err)
	}
	if !tray.Ready() {
		t.Fatal("initial tray is not ready")
	}

	// Model Explorer losing this registration from outside the owner thread.
	// Do not mutate owner-thread fields, broadcast messages, or touch other icons.
	if ret, _, _ := trayShellNotifyIcon.Call(trayNIMDelete, uintptr(unsafe.Pointer(&data))); ret == 0 {
		t.Fatal("remove own test icon failed")
	}
	if _, err := trayTestIconRect(data.Window); err == nil {
		t.Fatal("removed native icon still exists")
	}
	name := windows.StringToUTF16Ptr("TaskbarCreated")
	message, _, err := trayRegisterMessage.Call(uintptr(unsafe.Pointer(name)))
	if message == 0 {
		t.Fatal(trayNativeError("register test TaskbarCreated message", err))
	}
	trayTestPost(t, data.Window, uint32(message), 0, 0)
	trayTestWait(t, "native icon recovery and Ready", func() bool {
		_, err := trayTestIconRect(data.Window)
		return err == nil && tray.Ready()
	})
	if err := tray.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := trayTestIconRect(data.Window); err == nil {
		t.Fatal("recovered icon survived Stop")
	}
	if tray.Ready() {
		t.Fatal("stopped tray is ready")
	}
	select {
	case err := <-nativeErrors:
		t.Fatal("unexpected asynchronous native error:", err)
	default:
	}
}

func TestTrayNativeOpenHideAndQuitCallbacks(t *testing.T) {
	if os.Getenv("SHAREME_TRAY_TEST") != "1" {
		t.Skip("set SHAREME_TRAY_TEST=1 to exercise only this test's native tray commands (requires Explorer)")
	}
	callbacks := make(chan uint32, 16)
	nativeErrors := make(chan error, 16)
	tray, err := StartWindowsTray(TrayCallbacks{
		Open:  func() { callbacks <- trayCmdOpen },
		Hide:  func() { callbacks <- trayCmdHide },
		Quit:  func() { callbacks <- trayCmdQuit },
		Error: func(err error) { nativeErrors <- err },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tray.Stop(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	hwnd := tray.iconData().Window
	awaitCallback := func(expected uint32) {
		t.Helper()
		select {
		case actual := <-callbacks:
			if actual != expected {
				t.Fatalf("callback = %d, want %d", actual, expected)
			}
		case err := <-nativeErrors:
			t.Fatal("asynchronous native error:", err)
		case <-time.After(5 * time.Second):
			t.Fatalf("callback %d was not dispatched", expected)
		}
	}
	for _, event := range []uintptr{trayNINSelect, trayNINKeySelect} {
		trayTestPost(t, hwnd, trayWMCallback, 0, trayIconID<<16|event)
		awaitCallback(trayCmdOpen)
	}
	for _, command := range []uint32{trayCmdOpen, trayCmdHide, trayCmdQuit} {
		trayTestPost(t, hwnd, trayWMCommand, uintptr(command), 0)
		awaitCallback(command)
	}
	if err := tray.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-nativeErrors:
		t.Fatal("unexpected asynchronous native error:", err)
	default:
	}
}
