//go:build windows

package probe

import "os"

func supportedPlatform() bool { return true }
func readFlags() int          { return os.O_RDONLY }
