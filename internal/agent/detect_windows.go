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
	procCreateFileW             = kernel32.NewProc("CreateFileW")
	procDeviceIoControl         = kernel32.NewProc("DeviceIoControl")
	procCloseHandle             = kernel32.NewProc("CloseHandle")
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

const (
	fileShareRead  = 0x00000001
	fileShareWrite = 0x00000002
	openExisting   = 3

	ioctlStorageQueryProperty = 0x002D1400

	storageDeviceProperty = 0
	propertyStandardQuery = 0
	busTypeUnknown        = 0
	busTypeUSB            = 7
)

type storagePropertyQuery struct {
	PropertyId           uint32
	QueryType            uint32
	AdditionalParameters [1]byte
}

type storageDeviceDescriptor struct {
	Version               uint32
	Size                  uint32
	DeviceType            byte
	DeviceTypeModifier    byte
	RemovableMedia        byte
	CommandQueueing       byte
	VendorIdOffset        uint32
	ProductIdOffset       uint32
	ProductRevisionOffset uint32
	SerialNumberOffset    uint32
	BusType               uint32
	RawPropertiesLength   uint32
}

func detectVolumes() ([]MountedVolume, error) {
	drives, err := getDrives()
	if err != nil {
		return nil, err
	}

	out := make([]MountedVolume, 0)

	for _, root := range drives {
		dtype := getDriveType(root)

		// Ignore obvious non-local / unsupported roots.
		switch dtype {
		case driveUnknown, driveNoRootDir, driveRemote, driveCDROM, driveRAMDisk:
			continue
		}

		// Check config first to avoid unnecessary device queries.
		var cfgPath string
		for _, name := range []string{"porta.yml", ".porta.yml"} {
			candidate := filepath.Join(root, name)
			if _, err := os.Stat(candidate); err == nil {
				cfgPath = candidate
				break
			}
		}
		if cfgPath == "" {
			continue
		}

		// Strict USB-ish detection by querying storage bus type.
		// This handles many USB SSDs reported as DRIVE_FIXED.
		isUSB, err := isUSBStorage(root)
		if err != nil {
			continue
		}
		if !isUSB {
			continue
		}

		out = append(out, MountedVolume{
			Root:       strings.TrimRight(root, `\`),
			ConfigPath: cfgPath,
		})
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

func isUSBStorage(root string) (bool, error) {
	trimmed := strings.TrimRight(root, `\`)
	if len(trimmed) < 2 {
		return false, syscall.EINVAL
	}

	// Example: E:\ -> \\.\E:
	devicePath := `\\.\` + trimmed

	p, err := syscall.UTF16PtrFromString(devicePath)
	if err != nil {
		return false, err
	}

	handle, _, err := procCreateFileW.Call(
		uintptr(unsafe.Pointer(p)),
		0,
		fileShareRead|fileShareWrite,
		0,
		openExisting,
		0,
		0,
	)
	if handle == uintptr(syscall.InvalidHandle) {
		return false, err
	}
	defer procCloseHandle.Call(handle)

	query := storagePropertyQuery{
		PropertyId: storageDeviceProperty,
		QueryType:  propertyStandardQuery,
	}

	buf := make([]byte, 1024)
	var returned uint32

	r1, _, err := procDeviceIoControl.Call(
		handle,
		ioctlStorageQueryProperty,
		uintptr(unsafe.Pointer(&query)),
		unsafe.Sizeof(query),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&returned)),
		0,
	)
	if r1 == 0 {
		return false, err
	}

	if returned < uint32(unsafe.Sizeof(storageDeviceDescriptor{})) {
		return false, nil
	}

	desc := (*storageDeviceDescriptor)(unsafe.Pointer(&buf[0]))

	// Strict match for USB bus.
	if desc.BusType == busTypeUSB {
		return true, nil
	}

	// Fallback: if Windows reports removable media and bus type is unknown,
	// still allow it.
	if desc.BusType == busTypeUnknown && getDriveType(root) == driveRemovable {
		return true, nil
	}

	return false, nil
}
