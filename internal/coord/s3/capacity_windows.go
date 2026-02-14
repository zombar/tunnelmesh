//go:build windows

package s3

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GetVolumeStats returns filesystem statistics for the given path.
// Returns total, used, and available bytes.
func GetVolumeStats(path string) (total, used, available int64, err error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("utf16 path: %w", err)
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(
		pathPtr,
		(*uint64)(unsafe.Pointer(&freeBytesAvailable)),
		(*uint64)(unsafe.Pointer(&totalBytes)),
		(*uint64)(unsafe.Pointer(&totalFreeBytes)),
	); err != nil {
		return 0, 0, 0, fmt.Errorf("GetDiskFreeSpaceEx %s: %w", path, err)
	}

	total = int64(totalBytes)
	available = int64(freeBytesAvailable)
	used = total - int64(totalFreeBytes)
	return total, used, available, nil
}
