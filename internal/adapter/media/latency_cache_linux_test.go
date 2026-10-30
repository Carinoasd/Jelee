//go:build perf && linux

package media

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// dropPageCache asks the kernel to drop the clean cached pages of path; it
// reports whether the call succeeded (it is advice, not a guarantee).
func dropPageCache(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	return unix.Fadvise(int(file.Fd()), 0, 0, unix.FADV_DONTNEED) == nil
}

// residentPercent is the share of the file's pages currently in the page
// cache (mincore), or -1 when it cannot be determined.
func residentPercent(path string) float64 {
	file, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return -1
	}
	data, err := unix.Mmap(int(file.Fd()), 0, int(info.Size()), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return -1
	}
	defer unix.Munmap(data)
	pages := (len(data) + unix.Getpagesize() - 1) / unix.Getpagesize()
	vec := make([]byte, pages)
	if _, _, errno := unix.Syscall(unix.SYS_MINCORE, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&vec[0]))); errno != 0 {
		return -1
	}
	resident := 0
	for _, b := range vec {
		resident += int(b & 1)
	}
	return float64(resident) * 100 / float64(pages)
}

// filesystemName names the filesystem holding path by its statfs magic.
func filesystemName(path string) string {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return "unknown"
	}
	switch uint32(stat.Type) {
	case 0x01021994:
		return "tmpfs (page cache is the storage; cold rows are meaningless)"
	case 0xEF53:
		return "ext4"
	case 0x58465342:
		return "xfs"
	case 0x9123683E:
		return "btrfs"
	case 0x794c7630:
		return "overlayfs"
	case 0x01021997, 0x6969:
		return "9p/nfs (network or host share)"
	}
	return fmt.Sprintf("magic 0x%x", uint32(stat.Type))
}
