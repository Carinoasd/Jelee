//go:build linux

package media

import (
	"os"

	"golang.org/x/sys/unix"
)

// adviseWillNeed asks the kernel to start reading a range into the page
// cache without waiting for it. The advice cannot change file content.
func adviseWillNeed(file *os.File, offset, length int64) {
	if length <= 0 {
		return
	}
	// SyscallConn keeps the descriptor in non-blocking mode and pinned
	// against a concurrent Close, unlike Fd.
	raw, err := file.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = unix.Fadvise(int(fd), offset, length, unix.FADV_WILLNEED)
	})
}

// prefetchSupported reports whether adviseWillNeed reaches the kernel.
const prefetchSupported = true
