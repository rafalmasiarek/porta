//go:build windows

package agent

import (
	"context"
	"syscall"
	"time"
	"unsafe"
)

const (
	wmDeviceChange          = 0x0219
	dbtDeviceArrival        = 0x8000
	dbtDeviceRemoveComplete = 0x8004
	wmClose                 = 0x0010
	wmDestroy               = 0x0002
)

var (
	user32        = syscall.NewLazyDLL("user32.dll")
	kernel32Watch = syscall.NewLazyDLL("kernel32.dll")

	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procGetModuleHandleW = kernel32Watch.NewProc("GetModuleHandleW")
)

type point struct {
	X int32
	Y int32
}

type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

func watchVolumeChanges(ctx context.Context, trigger chan<- struct{}) error {
	var debounceTimer *time.Timer

	fireDebounced := func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(700*time.Millisecond, func() {
			select {
			case trigger <- struct{}{}:
			default:
			}
		})
	}

	className, _ := syscall.UTF16PtrFromString("PortaHiddenDeviceWindow")

	wndProc := syscall.NewCallback(func(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
		switch message {
		case wmDeviceChange:
			if wParam == dbtDeviceArrival || wParam == dbtDeviceRemoveComplete {
				fireDebounced()
			}
			return 0

		case wmClose:
			procDestroyWindow.Call(hwnd)
			return 0

		case wmDestroy:
			procPostQuitMessage.Call(0)
			return 0

		default:
			r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
			return r
		}
	})

	hInstance, _, _ := procGetModuleHandleW.Call(0)

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   wndProc,
		HInstance:     hInstance,
		LpszClassName: className,
	}

	r1, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if r1 == 0 {
		// If class already exists, Windows can still return 0 here.
		// We continue and try to create the window anyway.
		_ = err
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)),
		0,
		0, 0, 0, 0,
		0,
		0,
		hInstance,
		0,
	)
	if hwnd == 0 {
		return err
	}

	go func() {
		<-ctx.Done()
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}()

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	return nil
}
