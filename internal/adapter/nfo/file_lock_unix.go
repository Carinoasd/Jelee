//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockNameKey(name string) string { return name }
func lockOpenFlags() int             { return os.O_RDWR | os.O_CREATE | unix.O_NOFOLLOW | unix.O_NONBLOCK }
func safeLockInfo(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Size() == 0
}
func tryFileLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return err == nil, err
}
func unlockFile(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }
