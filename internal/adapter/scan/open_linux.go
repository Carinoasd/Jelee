//go:build linux

package scan

import (
	"os"
	"syscall"
)

func directoryFlags() int { return os.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NONBLOCK }

func supportedPlatform() bool { return true }
