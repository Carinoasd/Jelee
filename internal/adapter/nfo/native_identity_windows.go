//go:build windows

package nfo

import (
	"encoding/binary"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func observeNFONativeIdentity(file *os.File) (nfoNativeIdentity, error) {
	if file == nil {
		return nfoNativeIdentity{}, errNativeIdentity
	}
	control, err := file.SyscallConn()
	if err != nil {
		return nfoNativeIdentity{}, errNativeIdentity
	}
	var result nfoNativeIdentity
	var observationErr error
	err = control.Control(func(fd uintptr) {
		handle := windows.Handle(fd)
		kind, err := windows.GetFileType(handle)
		if err != nil || kind != windows.FILE_TYPE_DISK {
			observationErr = errNativeIdentity
			return
		}
		var info windows.ByHandleFileInformation
		if windows.GetFileInformationByHandle(handle, &info) != nil || info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
			observationErr = errNativeIdentity
			return
		}
		var id struct {
			Volume uint64
			FileID [16]byte
		}
		if windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))) != nil {
			observationErr = errNativeIdentity
			return
		}
		result.record[0], result.record[1], result.record[2] = 1, 1, 1
		if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
			result.record[2] = 2
		}
		binary.LittleEndian.PutUint64(result.record[8:16], id.Volume)
		copy(result.record[16:32], id.FileID[:])
		// Preserve native FILETIME ticks without an overflowing Unix conversion.
		birth := uint64(info.CreationTime.HighDateTime)<<32 | uint64(info.CreationTime.LowDateTime)
		binary.LittleEndian.PutUint64(result.record[32:40], birth)
	})
	if err != nil || observationErr != nil {
		return nfoNativeIdentity{}, errNativeIdentity
	}
	return result, nil
}
