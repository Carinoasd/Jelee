//go:build linux

package ignoresource

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeLinuxDescriptorFlags(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, ".jeleeignore"), []byte("*.tmp"), 0600); err != nil {
		t.Fatal("fixture write failed")
	}
	d, err := openNativeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f, err := d.OpenRule()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, file := range []*nativeFile{d.(*nativeFile), f.(*nativeFile)} {
		control, err := file.file.SyscallConn()
		if err != nil {
			t.Fatal("raw handle unavailable")
		}
		var flagErr error
		var fdFlags, statusFlags int
		err = control.Control(func(fd uintptr) {
			fdFlags, flagErr = unix.FcntlInt(fd, unix.F_GETFD, 0)
			if flagErr == nil {
				statusFlags, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
			}
		})
		if err != nil || flagErr != nil || fdFlags&unix.FD_CLOEXEC == 0 || statusFlags&unix.O_ACCMODE != unix.O_RDONLY || statusFlags&unix.O_NONBLOCK == 0 {
			t.Fatal("native handle lost readonly/CLOEXEC/nonblocking flags")
		}
	}
}

func TestNativeLinuxFailClosedErrorClassification(t *testing.T) {
	for _, err := range []error{unix.ENOSYS, unix.EINVAL, unix.E2BIG, unix.EOPNOTSUPP} {
		if nativeOpenError(err, false) != ErrUnavailable || nativeOpenError(err, true) != ErrUnavailable {
			t.Fatal("unsupported strict primitive did not fail unavailable")
		}
	}
	for _, err := range []error{unix.ELOOP, unix.EXDEV, unix.ENOTDIR, unix.EISDIR, unix.ENXIO, unix.ENODEV} {
		if nativeOpenError(err, true) != ErrUnsafe {
			t.Fatal("unsafe object was accepted or marked absent")
		}
	}
	if nativeOpenError(unix.ENOENT, true) != errAbsent || nativeOpenError(unix.ENOENT, false) != ErrRead || nativeOpenError(unix.EACCES, true) != ErrRead {
		t.Fatal("only a missing fixed leaf may be absent")
	}
}
