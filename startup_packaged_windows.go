package main

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A Microsoft Store (MSIX) process has its HKCU writes redirected to a private hive, so a Run
// entry would never start anything. Windows starts packaged apps through the startup task
// declared in the package manifest, which packagedStartupStore turns on and off with the
// Windows.ApplicationModel.StartupTask API.
type packagedStartupStore struct{}

const (
	startupTaskDisabledByUser  = 1
	startupTaskEnabled         = 2
	startupTaskEnabledByPolicy = 4

	// Method table slots. IUnknown and IInspectable occupy 0 to 5.
	slotQueryInterface     = 0
	slotRelease            = 2
	slotStaticsGetAsync    = 7
	slotTaskRequestEnable  = 6
	slotTaskDisable        = 7
	slotTaskState          = 8
	slotAsyncInfoStatus    = 7
	slotAsyncInfoErrorCode = 8
	slotOperationResults   = 8
)

var (
	startupCombase        = windows.NewLazySystemDLL("combase.dll")
	startupRoInitialize   = startupCombase.NewProc("RoInitialize")
	startupRoUninitialize = startupCombase.NewProc("RoUninitialize")
	startupRoGetFactory   = startupCombase.NewProc("RoGetActivationFactory")
	startupCreateString   = startupCombase.NewProc("WindowsCreateString")
	startupDeleteString   = startupCombase.NewProc("WindowsDeleteString")
	startupPackageName    = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentPackageFullName")

	iidStartupTaskStatics = windows.GUID{Data1: 0xee5b60bd, Data2: 0xa148, Data3: 0x41a7, Data4: [8]byte{0xb2, 0x6e, 0xe8, 0xb8, 0x8a, 0x1e, 0x62, 0xf8}}
	iidAsyncInfo          = windows.GUID{Data1: 0x00000036, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}

	// runningPackaged reports whether this process has package identity.
	runningPackaged = sync.OnceValue(func() bool {
		var length uint32
		result, _, _ := startupPackageName.Call(uintptr(unsafe.Pointer(&length)), 0)
		return windows.Errno(result) != windows.APPMODEL_ERROR_NO_PACKAGE
	})
)

// comObject is a COM interface pointer: its first word points at the method table.
type comObject struct{ methods *[11]uintptr }

// call passes pointers as uintptr arguments, so the compiler must keep them alive and unmoved.
//
//go:uintptrescapes
func (object *comObject) call(operation string, slot int, arguments ...uintptr) error {
	result, _, _ := syscall.SyscallN(object.methods[slot], append([]uintptr{uintptr(unsafe.Pointer(object))}, arguments...)...)
	return startupResult(operation, result)
}

func (object *comObject) release() {
	if object != nil {
		syscall.SyscallN(object.methods[slotRelease], uintptr(unsafe.Pointer(object)))
	}
}

func startupResult(operation string, result uintptr) error {
	if int32(result) >= 0 {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, windows.Errno(uint32(result)))
}

func startupString(value string) (windows.Handle, error) {
	text, err := windows.UTF16FromString(value)
	if err != nil {
		return 0, err
	}
	var handle windows.Handle
	result, _, _ := startupCreateString.Call(uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)-1), uintptr(unsafe.Pointer(&handle)))
	return handle, startupResult("create Windows Runtime string", result)
}

// awaitStartup waits for a Windows Runtime operation by polling, which needs no callback object.
func awaitStartup(operation *comObject, value unsafe.Pointer) error {
	var info *comObject
	if err := operation.call("read startup request", slotQueryInterface, uintptr(unsafe.Pointer(&iidAsyncInfo)), uintptr(unsafe.Pointer(&info))); err != nil {
		return err
	}
	defer info.release()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var status int32
		if err := info.call("read startup request", slotAsyncInfoStatus, uintptr(unsafe.Pointer(&status))); err != nil {
			return err
		}
		switch status {
		case 0: // Started
			if time.Now().After(deadline) {
				return errors.New("Windows did not answer the startup request")
			}
		case 1: // Completed
			return operation.call("read startup result", slotOperationResults, uintptr(value))
		default:
			var code int32
			if err := info.call("read startup failure", slotAsyncInfoErrorCode, uintptr(unsafe.Pointer(&code))); err != nil {
				return err
			}
			return fmt.Errorf("Windows startup request failed: %w", windows.Errno(uint32(code)))
		}
	}
}

func withStartupTask(use func(task *comObject) error) error {
	done := make(chan error, 1)
	go func() {
		// The Windows Runtime is initialized per thread. The goroutine never unlocks the thread,
		// so Go discards the thread with it.
		runtime.LockOSThread()
		done <- func() error {
			const multithreaded, changedMode = 1, 0x80010106
			result, _, _ := startupRoInitialize.Call(multithreaded)
			if int32(result) >= 0 {
				defer startupRoUninitialize.Call()
			} else if uint32(result) != changedMode { // Already initialized another way: still usable.
				return startupResult("start Windows Runtime", result)
			}
			class, err := startupString("Windows.ApplicationModel.StartupTask")
			if err != nil {
				return err
			}
			defer startupDeleteString.Call(uintptr(class))
			id, err := startupString(startupTaskID)
			if err != nil {
				return err
			}
			defer startupDeleteString.Call(uintptr(id))
			var statics, operation, task *comObject
			result, _, _ = startupRoGetFactory.Call(uintptr(class), uintptr(unsafe.Pointer(&iidStartupTaskStatics)), uintptr(unsafe.Pointer(&statics)))
			if err := startupResult("open Windows startup tasks", result); err != nil {
				return err
			}
			defer statics.release()
			if err := statics.call("find the Share Me startup task", slotStaticsGetAsync, uintptr(id), uintptr(unsafe.Pointer(&operation))); err != nil {
				return err
			}
			defer operation.release()
			if err := awaitStartup(operation, unsafe.Pointer(&task)); err != nil {
				return err
			}
			defer task.release()
			return use(task)
		}()
	}()
	return <-done
}

func (packagedStartupStore) Read() (startupRegistration, error) {
	var state int32
	err := withStartupTask(func(task *comObject) error {
		return task.call("read Windows startup state", slotTaskState, uintptr(unsafe.Pointer(&state)))
	})
	return startupRegistration{Exists: state == startupTaskEnabled || state == startupTaskEnabledByPolicy}, err
}

func (packagedStartupStore) Write(value startupRegistration) error {
	return withStartupTask(func(task *comObject) error {
		if !value.Exists {
			return task.call("turn off Windows startup", slotTaskDisable)
		}
		var operation *comObject
		if err := task.call("turn on Windows startup", slotTaskRequestEnable, uintptr(unsafe.Pointer(&operation))); err != nil {
			return err
		}
		defer operation.release()
		var state int32
		if err := awaitStartup(operation, unsafe.Pointer(&state)); err != nil {
			return err
		}
		switch state {
		case startupTaskEnabled, startupTaskEnabledByPolicy:
			return nil
		case startupTaskDisabledByUser:
			return errors.New("Windows has startup turned off for Share Me. Turn it on in Windows Settings > Apps > Startup, then try again")
		default:
			return errors.New("Windows or your organization does not allow Share Me to start with Windows")
		}
	})
}
