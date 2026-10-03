//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package nfo

import "os"

func lockNameKey(name string) string          { return name }
func lockOpenFlags() int                      { return os.O_RDWR | os.O_CREATE }
func safeLockInfo(info os.FileInfo) bool      { return false }
func tryFileLock(file *os.File) (bool, error) { return false, ErrFileLock }
func unlockFile(file *os.File) error          { return ErrFileLock }
