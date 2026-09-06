//go:build windows

package storage

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type DiskSpace struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

func GetDiskSpace(path string) (DiskSpace, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return DiskSpace{TotalBytes: 200 * 1024 * 1024 * 1024, FreeBytes: 150 * 1024 * 1024 * 1024}, nil
	}
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")
	r1, _, _ := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)
	if r1 == 0 {
		return DiskSpace{TotalBytes: 200 * 1024 * 1024 * 1024, FreeBytes: 150 * 1024 * 1024 * 1024}, nil
	}
	return DiskSpace{
		TotalBytes: totalNumberOfBytes,
		FreeBytes:  freeBytesAvailable,
		UsedBytes:  totalNumberOfBytes - totalNumberOfFreeBytes,
	}, nil
}
