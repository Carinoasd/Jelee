//go:build windows

package scan

import "os"

func directoryFlags() int { return os.O_RDONLY }

func supportedPlatform() bool { return true }
