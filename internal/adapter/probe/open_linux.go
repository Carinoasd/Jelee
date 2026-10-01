//go:build linux

package probe

import (
	"os"
	"syscall"
)

func supportedPlatform() bool { return true }

// A FIFO substituted after Lstat must not block the opening thread. Stat on
// the resulting handle still rejects it, along with every other special file.
func readFlags() int { return os.O_RDONLY | syscall.O_NONBLOCK }
