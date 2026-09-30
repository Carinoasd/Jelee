//go:build linux

package media

import (
	"os"
	"syscall"
)

// O_NONBLOCK makes a malicious FIFO fail the regular-file check instead of
// blocking before cancellation can reach the file. It has no effect on files.
func readOnlyFlags() int { return os.O_RDONLY | syscall.O_NONBLOCK }
