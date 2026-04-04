//go:build windows

package agent

import (
    "os"
    "path/filepath"
    "sort"
    "strings"
    "syscall"
    "unsafe"
)

var (
    kernel32 = syscall.NewLazyDLL("kernel32.dll")

    procGetLogicalDriveStringsW = kernel32.NewProc("GetLogicalDriveStringsW")
    procGetDriveTypeW           = kernel32.NewProc("GetDriveTypeW")
)

const (
    driveUnknown   = 0
    driveNoRootDir = 1
    driveRemovable = 2
    driveFixed     = 3
    driveRemote    = 4
    driveCDROM     = 5
    driveRAMDisk   = 6
)

func detectVolumes() ([]MountedVolume, error) {
    drives, err := getDrives()
    if err != nil {
        return nil, err
    }

    out := make([]MountedVolume, 0)

    for _, root := range drives {
        dtype := getDriveType(root)

        // 🔥 STRICT USB FILTER
        // removable ONLY (bez fixed!)
        if dtype != driveRemovable {
            continue
        }

        for _, name := range []string{"porta.yml", ".porta.yml"} {
            cfg := filepath.Join(root, name)

            if _, err := os.Stat(cfg); err == nil {
                out = append(out, MountedVolume{
                    Root:       strings.TrimRight(root, "\\"),
                    ConfigPath: cfg,
                })
                break
            }
        }
    }

    sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
    return out, nil
}

func getDrives() ([]string, error) {
    r0, _, err := procGetLogicalDriveStringsW.Call(0, 0)
    if r0 == 0 {
        return nil, err
    }

    buf := make([]uint16, r0)

    r1, _, err := procGetLogicalDriveStringsW.Call(
        uintptr(len(buf)),
        uintptr(unsafe.Pointer(&buf[0])),
    )

    if r1 == 0 {
        return nil, err
    }

    var drives []string
    start := 0

    for i, v := range buf {
        if v == 0 {
            if i == start {
                break
            }
            drives = append(drives, syscall.UTF16ToString(buf[start:i]))
            start = i + 1
        }
    }

    return drives, nil
}

func getDriveType(root string) uint32 {
    p, _ := syscall.UTF16PtrFromString(root)
    r0, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(p)))
    return uint32(r0)
}