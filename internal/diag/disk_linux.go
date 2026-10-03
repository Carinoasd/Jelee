//go:build linux

package diag

import "golang.org/x/sys/unix"

func statfs(path string) (DiskUsage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return DiskUsage{}, errDiskUnavailable
	}
	size := uint64(st.Bsize)
	return DiskUsage{
		TotalBytes: st.Blocks * size, FreeBytes: st.Bavail * size,
		TotalInodes: st.Files, FreeInodes: st.Ffree, InodesKnown: true,
	}, nil
}
