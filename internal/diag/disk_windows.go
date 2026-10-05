//go:build windows

package diag

import "golang.org/x/sys/windows"

// Windows reports bytes only; NTFS has no fixed inode table.
func statfs(path string) (DiskUsage, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return DiskUsage{}, errDiskUnavailable
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(name, &available, &total, &free); err != nil {
		return DiskUsage{}, errDiskUnavailable
	}
	return DiskUsage{TotalBytes: total, FreeBytes: available}, nil
}
