//go:build linux

package nfo

import (
	"os"
	"syscall"
)

func readOnlyFlags() int { return os.O_RDONLY | syscall.O_NONBLOCK }
