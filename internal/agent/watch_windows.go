//go:build windows

package agent

import (
    "context"
    "sync"
    "syscall"
    "time"
)

const (
    wmDeviceChange = 0x0219
    dbtArrival     = 0x8000
    dbtRemove      = 0x8004
)

var (
    user32              = syscall.NewLazyDLL("user32.dll")
    procGetMessageW     = user32.NewProc("GetMessageW")
    procDispatchMessage = user32.NewProc("DispatchMessageW")
    procDefWindowProc   = user32.NewProc("DefWindowProcW")
)

type msg struct {
    HWnd    uintptr
    Message uint32
    WParam  uintptr
    LParam  uintptr
    Time    uint32
    Pt      struct{ X, Y int32 }
}

func watchVolumeChanges(ctx context.Context, trigger chan<- struct{}) error {

    var mu sync.Mutex
    var timer *time.Timer

    debounce := func() {
        mu.Lock()
        defer mu.Unlock()

        if timer != nil {
            timer.Stop()
        }

        timer = time.AfterFunc(500*time.Millisecond, func() {
            select {
            case trigger <- struct{}{}:
            default:
            }
        })
    }

    callback := syscall.NewCallback(func(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {

        if msg == wmDeviceChange {
            if wparam == dbtArrival || wparam == dbtRemove {
                debounce()
            }
        }

        r, _, _ := procDefWindowProc.Call(hwnd, uintptr(msg), wparam, lparam)
        return r
    })

    _ = callback // keep alive

    var m msg

    for {
        select {
        case <-ctx.Done():
            return nil
        default:
            ret, _, _ := procGetMessageW.Call(uintptr(&m), 0, 0, 0)
            if int32(ret) <= 0 {
                return nil
            }
            procDispatchMessage.Call(uintptr(&m))
        }
    }
}