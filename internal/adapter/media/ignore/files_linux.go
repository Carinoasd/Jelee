//go:build linux

package ignoresource

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// nativeFile owns a CLOEXEC, read-only handle. Control keeps its descriptor
// alive across native calls even when the reader's cancellation closes it.
type nativeFile struct {
	file     *os.File
	once     sync.Once
	closeErr error
}

func openNativeRoot(path string) (directory, error) {
	if len(path) > MaxRootBytes || !filepath.IsAbs(path) || !utf8.ValidString(path) || strings.ContainsFunc(path, unicode.IsControl) {
		return nil, ErrInvalid
	}
	// Root is an absolute trusted configuration value. NO_SYMLINKS covers its
	// entire pathname, including the root itself; BENEATH is for relative opens.
	opened, err := nativeOpen(unix.AT_FDCWD, filepath.Clean(path), true, false, false)
	if err != nil {
		return nil, err
	}
	return opened, nil
}

func nativeOpen(parent int, name string, directoryOnly, beneath, allowAbsent bool) (*nativeFile, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK
	if directoryOnly {
		flags |= unix.O_DIRECTORY
	}
	resolve := uint64(unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS)
	if beneath {
		resolve |= unix.RESOLVE_BENEATH
	}
	fd, err := unix.Openat2(parent, name, &unix.OpenHow{Flags: uint64(flags), Resolve: resolve})
	if err != nil {
		return nil, nativeOpenError(err, allowAbsent)
	}
	f := &nativeFile{file: os.NewFile(uintptr(fd), "ignore source")}
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
	if allowAbsent && errors.Is(err, unix.ENOENT) {
		return errAbsent
	}
	switch {
	case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EINVAL), errors.Is(err, unix.E2BIG), errors.Is(err, unix.EOPNOTSUPP):
		return ErrUnavailable
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV), errors.Is(err, unix.ENOTDIR), errors.Is(err, unix.EISDIR), errors.Is(err, unix.ENXIO), errors.Is(err, unix.ENODEV):
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
	control, err := f.file.SyscallConn()
	if err != nil {
		return nil, ErrRead
	}
	var opened *nativeFile
	var openErr error
	err = control.Control(func(fd uintptr) {
		opened, openErr = nativeOpen(int(fd), name, directoryOnly, true, allowAbsent)
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

func (f *nativeFile) Stat() (fileState, error) {
	if f == nil || f.file == nil {
		return fileState{}, ErrRead
	}
	control, err := f.file.SyscallConn()
	if err != nil {
		return fileState{}, ErrRead
	}
	var stat unix.Stat_t
	var statErr error
	err = control.Control(func(fd uintptr) { statErr = unix.Fstat(int(fd), &stat) })
	if err != nil || statErr != nil {
		return fileState{}, ErrRead
	}
	return linuxFileState(stat)
}

func linuxFileState(stat unix.Stat_t) (fileState, error) {
	if stat.Size < 0 || stat.Mtim.Nsec < 0 || stat.Mtim.Nsec >= 1_000_000_000 {
		return fileState{}, ErrLimit
	}
	modified := time.Unix(int64(stat.Mtim.Sec), int64(stat.Mtim.Nsec))
	if !time.Unix(0, modified.UnixNano()).Equal(modified) {
		return fileState{}, ErrLimit
	}
	state := fileState{size: stat.Size, modifiedUnixNano: modified.UnixNano(), kind: nodeOther}
	state.identity[0] = 1 // Linux device + full inode number, fixed little endian.
	binary.LittleEndian.PutUint64(state.identity[8:16], uint64(stat.Dev))
	binary.LittleEndian.PutUint64(state.identity[16:24], uint64(stat.Ino))
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		state.kind = nodeRegular
	case unix.S_IFDIR:
		state.kind = nodeDirectory
	}
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
