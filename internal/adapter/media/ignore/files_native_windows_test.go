//go:build windows

package ignoresource

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestNativeWindowsRejectsTimestampOverflow(t *testing.T) {
	for _, year := range []int{1650, 2500} {
		root := filepath.Clean(t.TempDir())
		path := filepath.Join(root, ".jeleeignore")
		if err := os.WriteFile(path, []byte("*.tmp"), 0600); err != nil {
			t.Fatal("fixture write failed")
		}
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal("fixture handle failed")
		}
		// Set the native FILETIME directly: time.Time.UnixNano would overflow
		// before the test reached the reader's own range checks.
		ticks := uint64(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()+11644473600) * 10000000
		stamp := windows.Filetime{LowDateTime: uint32(ticks), HighDateTime: uint32(ticks >> 32)}
		control, err := f.SyscallConn()
		if err != nil {
			f.Close()
			t.Fatal("fixture raw handle failed")
		}
		var setErr error
		err = control.Control(func(handle uintptr) { setErr = windows.SetFileTime(windows.Handle(handle), nil, nil, &stamp) })
		closeErr := f.Close()
		if err != nil || setErr != nil || closeErr != nil {
			t.Fatal("native out-of-range timestamp fixture failed")
		}
		d, err := openNativeRoot(root)
		if err != nil {
			t.Fatal("native root failed:", err)
		}
		opened, err := d.OpenRule()
		closeErr = d.Close()
		if err != ErrLimit || opened != nil || closeErr != nil {
			t.Fatal("native timestamp overflow accepted or leaked partial result")
		}
		original, err := os.ReadFile(path)
		if err != nil || string(original) != "*.tmp" {
			t.Fatal("timestamp validation modified source bytes")
		}
	}
}

func TestNativeWindowsFailClosedErrorClassification(t *testing.T) {
	for _, err := range []error{windows.STATUS_NOT_IMPLEMENTED, windows.STATUS_NOT_SUPPORTED, windows.STATUS_INVALID_PARAMETER} {
		if nativeOpenError(err, false) != ErrUnavailable || nativeOpenError(err, true) != ErrUnavailable {
			t.Fatal("unsupported strict primitive did not fail unavailable")
		}
	}
	for _, err := range []error{windows.STATUS_REPARSE_POINT_ENCOUNTERED, windows.STATUS_STOPPED_ON_SYMLINK, windows.STATUS_IO_REPARSE_TAG_NOT_HANDLED, windows.STATUS_REPARSE_POINT_NOT_RESOLVED, windows.STATUS_NOT_A_DIRECTORY, windows.STATUS_FILE_IS_A_DIRECTORY} {
		if nativeOpenError(err, true) != ErrUnsafe {
			t.Fatal("unsafe object was accepted or marked absent")
		}
	}
	if nativeOpenError(windows.STATUS_OBJECT_NAME_NOT_FOUND, true) != errAbsent || nativeOpenError(windows.STATUS_OBJECT_NAME_NOT_FOUND, false) != ErrRead || nativeOpenError(windows.STATUS_OBJECT_PATH_NOT_FOUND, true) != ErrRead || nativeOpenError(windows.STATUS_ACCESS_DENIED, true) != ErrRead {
		t.Fatal("only a missing fixed leaf may be absent")
	}
}
