//go:build !windows

package video_preprocess_setting

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// diskSpaceForPath reports the total and available bytes of the filesystem
// holding path. It walks up to the nearest existing ancestor so a temp
// directory that has not been created yet still resolves to its mount.
func diskSpaceForPath(path string) (total uint64, free uint64, ok bool) {
	for dir := path; ; {
		var stat unix.Statfs_t
		if err := unix.Statfs(dir, &stat); err == nil {
			bsize := uint64(stat.Bsize)
			return uint64(stat.Blocks) * bsize, uint64(stat.Bavail) * bsize, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return 0, 0, false
		}
		dir = parent
	}
}
