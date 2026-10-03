//go:build !linux && !windows

package diag

func statfs(string) (DiskUsage, error) { return DiskUsage{}, errDiskUnavailable }
