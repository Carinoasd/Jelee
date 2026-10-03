package nfo

import (
	"errors"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func lockNameKey(name string) string { return strings.ToLower(name) }
func lockOpenFlags() int             { return os.O_RDWR | os.O_CREATE }
func safeLockInfo(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return false
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}
func tryFileLock(file *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
func unlockFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}
