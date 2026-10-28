//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// openSharedLogFile opens a log file for reading with FILE_SHARE_DELETE, so the
// server can still rotate (rename) the active file while it is read or
// followed. os.Open omits that share mode on Windows.
func openSharedLogFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
