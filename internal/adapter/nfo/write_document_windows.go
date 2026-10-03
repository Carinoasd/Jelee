package nfo

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func regularNFOFile(info os.FileInfo) bool {
	if !validSourceInfo(info) {
		return false
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}

// Windows does not expose POSIX directory fsync via os.File.Sync. Data files
// are flushed before rename; power-loss durability of directory metadata is
// not established by this primitive and remains an acceptance requirement.
func syncNFODirectory(root *os.Root) error { return nil }
