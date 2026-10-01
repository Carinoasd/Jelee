//go:build windows

package ignoresource

import (
	"errors"
	"io"
	"os"
	"time"
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
	entries, readErr := f.file.ReadDir(directoryPageSize)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, ErrRead
	}
	result := make([]directoryEntry, 0, len(entries))
	for _, entry := range entries {
		// Go's Windows ReadDir stores metadata returned by the directory
		// handle query inside the DirEntry; Info does not re-open its path.
		info, err := entry.Info()
		if err != nil {
			return nil, ErrRead
		}
		modified := info.ModTime().UnixNano()
		if info.Size() < 0 || !time.Unix(0, modified).Equal(info.ModTime()) {
			return nil, ErrLimit
		}
		state := fileState{kind: nodeOther, size: info.Size(), modifiedUnixNano: modified}
		switch info.Mode().Type() {
		case 0:
			state.kind = nodeRegular
		case os.ModeDir:
			state.kind = nodeDirectory
		}
		result = append(result, directoryEntry{name: entry.Name(), state: state})
	}
	return result, readErr
}
