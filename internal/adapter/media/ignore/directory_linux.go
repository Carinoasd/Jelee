//go:build linux

package ignoresource

import (
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

var _ scanDirectory = (*nativeFile)(nil)

func (f *nativeFile) OpenDirectoryOrAbsent(name string) (directory, bool, error) {
	if !validNativeComponent(name) {
		return nil, false, ErrInvalid
	}
	opened, err := f.open(name, true, true)
	if errors.Is(err, errAbsent) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return opened, false, nil
}

func (f *nativeFile) ReadEntries() ([]directoryEntry, error) {
	if f == nil || f.file == nil {
		return nil, ErrRead
	}
	entries, readErr := f.file.Readdirnames(directoryPageSize)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, ErrRead
	}
	control, err := f.file.SyscallConn()
	if err != nil {
		return nil, ErrRead
	}
	result := make([]directoryEntry, 0, len(entries))
	for _, name := range entries {
		if !validNativeComponent(name) {
			result = append(result, directoryEntry{name: name, state: fileState{kind: nodeOther}})
			continue
		}
		var stat unix.Stat_t
		var statErr error
		// DirEntry.Info on an os.NewFile with a redacted name would resolve
		// relative to that name. Use the held descriptor explicitly instead.
		err = control.Control(func(fd uintptr) { statErr = unix.Fstatat(int(fd), name, &stat, unix.AT_SYMLINK_NOFOLLOW) })
		if err != nil || statErr != nil {
			return nil, ErrRead
		}
		state, err := linuxFileState(stat)
		if err != nil {
			return nil, err
		}
		result = append(result, directoryEntry{name: name, state: state})
	}
	return result, readErr
}
