//go:build windows

package ignoresource

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeFile struct {
	file     *os.File
	once     sync.Once
	closeErr error
}

// x/sys exports the allocating conversion but not its matching void release.
// Resolve the fixed system entry point before making that allocation.
var nativeFreeUnicode = windows.NewLazySystemDLL("ntdll.dll").NewProc("RtlFreeUnicodeString")

func openNativeRoot(path string) (directory, error) {
	if len(path) > MaxRootBytes || !filepath.IsAbs(path) || !utf8.ValidString(path) || strings.ContainsFunc(path, unicode.IsControl) {
		return nil, ErrInvalid
	}
	dos, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return nil, ErrInvalid
	}
	if nativeFreeUnicode.Find() != nil {
		return nil, ErrUnavailable
	}
	var name windows.NTUnicodeString
	if windows.RtlDosPathNameToNtPathName(dos, &name, nil, nil) != nil {
		return nil, ErrInvalid
	}
	defer func() { _, _, _ = nativeFreeUnicode.Call(uintptr(unsafe.Pointer(&name))) }() //nolint:gosec // G103: the Windows API takes a raw pointer to this fixed-layout value
	opened, err := nativeOpen(0, &name, true, false)
	if err != nil {
		return nil, err
	}
	return opened, nil
}

func nativeOpen(parent windows.Handle, name *windows.NTUnicodeString, directoryOnly, allowAbsent bool) (*nativeFile, error) {
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: name,
		Attributes: windows.OBJ_DONT_REPARSE | windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	options := uint32(windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_NON_DIRECTORY_FILE)
	if directoryOnly {
		options = windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_DIRECTORY_FILE
	}
	var handle windows.Handle
	err := windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ, &attributes, &windows.IO_STATUS_BLOCK{}, nil,
		0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, options, 0, 0)
	if err != nil {
		return nil, nativeOpenError(err, allowAbsent)
	}
	// OBJ_INHERIT is deliberately absent. No access grants writes, deletion,
	// execution, ownership or ACL changes. Only FILE_OPEN can open an existing object.
	f := &nativeFile{file: os.NewFile(uintptr(handle), "ignore source")}
	state, err := f.Stat()
	want := nodeRegular
	if directoryOnly {
		want = nodeDirectory
	}
	if err == nil && state.kind != want {
		err = ErrUnsafe
	}
	if err != nil {
		if f.Close() != nil {
			return nil, ErrRead
		}
		return nil, err
	}
	return f, nil
}

func nativeOpenError(err error, allowAbsent bool) error {
	if allowAbsent && errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) {
		return errAbsent
	}
	switch {
	case errors.Is(err, windows.STATUS_NOT_IMPLEMENTED), errors.Is(err, windows.STATUS_NOT_SUPPORTED), errors.Is(err, windows.STATUS_INVALID_PARAMETER):
		return ErrUnavailable
	case errors.Is(err, windows.STATUS_REPARSE_POINT_ENCOUNTERED), errors.Is(err, windows.STATUS_STOPPED_ON_SYMLINK),
		errors.Is(err, windows.STATUS_IO_REPARSE_TAG_NOT_HANDLED), errors.Is(err, windows.STATUS_REPARSE_POINT_NOT_RESOLVED),
		errors.Is(err, windows.STATUS_NOT_A_DIRECTORY), errors.Is(err, windows.STATUS_FILE_IS_A_DIRECTORY):
		return ErrUnsafe
	default:
		return ErrRead
	}
}

func validNativeComponent(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 1024 && utf8.ValidString(name) &&
		!strings.ContainsAny(name, "/\\:") && !strings.ContainsFunc(name, unicode.IsControl) && filepath.IsLocal(name)
}

func (f *nativeFile) open(name string, directoryOnly, allowAbsent bool) (*nativeFile, error) {
	if f == nil || f.file == nil {
		return nil, ErrRead
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, ErrInvalid
	}
	control, err := f.file.SyscallConn()
	if err != nil {
		return nil, ErrRead
	}
	var opened *nativeFile
	var openErr error
	err = control.Control(func(handle uintptr) {
		opened, openErr = nativeOpen(windows.Handle(handle), objectName, directoryOnly, allowAbsent)
	})
	if err != nil {
		return nil, ErrRead
	}
	return opened, openErr
}

func (f *nativeFile) OpenDirectory(name string) (directory, error) {
	if !validNativeComponent(name) {
		return nil, ErrInvalid
	}
	opened, err := f.open(name, true, false)
	if err != nil {
		return nil, err
	}
	return opened, nil
}

// FileIdInfo contains a 64-bit volume serial and the full 128-bit file ID.
// GetFileInformationByHandle's older 64-bit file index is not a substitute.
type nativeIDInfo struct {
	volume uint64
	id     [16]byte
}

func (f *nativeFile) Stat() (fileState, error) {
	if f == nil || f.file == nil {
		return fileState{}, ErrRead
	}
	control, err := f.file.SyscallConn()
	if err != nil {
		return fileState{}, ErrRead
	}
	var state fileState
	var statErr error
	err = control.Control(func(value uintptr) { state, statErr = nativeStat(windows.Handle(value)) })
	if err != nil {
		return fileState{}, ErrRead
	}
	return state, statErr
}

func nativeStat(handle windows.Handle) (fileState, error) {
	typeID, err := windows.GetFileType(handle)
	if err != nil {
		return fileState{}, ErrRead
	}
	if typeID != windows.FILE_TYPE_DISK {
		return fileState{}, ErrUnsafe
	}
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &info) != nil {
		return fileState{}, ErrRead
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return fileState{}, ErrUnsafe
	}
	var id nativeIDInfo
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))); err != nil { //nolint:gosec // G103: the Windows API takes a raw pointer to this fixed-layout value
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_CALL_NOT_IMPLEMENTED) {
			return fileState{}, ErrUnavailable
		}
		return fileState{}, ErrRead
	}
	size := uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)
	if size > math.MaxInt64 {
		return fileState{}, ErrLimit
	}
	// Convert native 100 ns FILETIME only after proving Unix nanoseconds fit;
	// Filetime.Nanoseconds itself permits int64 overflow for old/future dates.
	ticks := uint64(info.LastWriteTime.HighDateTime)<<32 | uint64(info.LastWriteTime.LowDateTime)
	const epoch = uint64(116444736000000000)
	var modified int64
	if ticks >= epoch {
		if ticks-epoch > math.MaxInt64/100 {
			return fileState{}, ErrLimit
		}
		modified = int64((ticks - epoch) * 100)
	} else {
		if epoch-ticks > math.MaxInt64/100 {
			return fileState{}, ErrLimit
		}
		modified = -int64((epoch - ticks) * 100)
	}
	state := fileState{size: int64(size), modifiedUnixNano: modified, kind: nodeRegular}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		state.kind = nodeDirectory
	}
	state.identity[0] = 2
	binary.LittleEndian.PutUint64(state.identity[8:16], id.volume)
	copy(state.identity[16:], id.id[:])
	return state, nil
}

func (f *nativeFile) Read(p []byte) (int, error) {
	if f == nil || f.file == nil {
		return 0, ErrRead
	}
	n, err := f.file.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, ErrRead
	}
	return n, err
}

func (f *nativeFile) Close() error {
	if f == nil || f.file == nil {
		return nil
	}
	f.once.Do(func() {
		if f.file.Close() != nil {
			f.closeErr = ErrRead
		}
	})
	return f.closeErr
}
