package engine

import "syscall"

// diskFreeSpace returns the available bytes on the filesystem containing path.
func diskFreeSpace(path string) int64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return -1
	}
	//nolint:unconvert // Bavail type varies by platform
	return int64(stat.Bavail) * int64(stat.Bsize)
}
