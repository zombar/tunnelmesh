//go:build !windows

package s3

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// GetVolumeStats returns filesystem statistics for the given path.
// Returns total, used, and available bytes. Available uses Bavail (non-root available space).
func GetVolumeStats(path string) (total, used, available int64, err error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, 0, 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	bsize := int64(stat.Bsize)
	total = int64(stat.Blocks) * bsize
	available = int64(stat.Bavail) * bsize
	used = total - int64(stat.Bfree)*bsize
	return total, used, available, nil
}
