//go:build !linux && !windows

package toolidentity

import (
	"errors"
	"os"
)

func readFlags() int                      { return os.O_RDONLY }
func safeFileInfo(os.FileInfo, bool) bool { return false }
func makePrivate(string) error            { return errors.New("identity_platform_unsupported") }
