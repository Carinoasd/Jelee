//go:build linux

package scan

import (
	"os"
	"syscall"
)

func directoryFlags() int { return os.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NONBLOCK }

func supportedPlatform() bool { return true }

// O_NONBLOCK makes a FIFO swapped in for a regular file fail the type check
// instead of blocking the open.
func fileFlags() int { return os.O_RDONLY | syscall.O_NONBLOCK }
