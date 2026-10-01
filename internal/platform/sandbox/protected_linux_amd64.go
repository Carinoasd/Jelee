package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func verifyHelperExecutable(path string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path || !validPath(path) {
		return ErrUnavailable
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), "approved helper executable")
	defer file.Close()
	if verifyProtectedFile(pinnedELF{file: file, path: path}) != nil {
		return ErrUnavailable
	}
	current, err := unix.Open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnavailable
	}
	defer unix.Close(current)
	var registered, running unix.Stat_t
	if unix.Fstat(fd, &registered) != nil || unix.Fstat(current, &running) != nil || registered.Dev != running.Dev || registered.Ino != running.Ino {
		return ErrUnavailable
	}
	return nil
}

// verifyProtectedFile checks the pinned inode and each canonical ancestor.
// Root is a trusted deployment authority; the service must be an unprivileged
// non-root identity. This does not freeze bytes against a privileged host.
func verifyProtectedFile(file pinnedELF) error {
	if file.file == nil || !validPath(file.path) || unix.Getuid() == 0 || unix.Geteuid() == 0 {
		return ErrUnavailable
	}
	var target unix.Stat_t
	if unix.Fstat(int(file.file.Fd()), &target) != nil || !protectedMode(target, false) || !writeDenied(int(file.file.Fd())) {
		return ErrUnavailable
	}
	current, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = unix.Close(current) }()
	parts := strings.Split(strings.TrimPrefix(file.path, "/"), "/")
	for index := 0; ; index++ {
		var ancestor unix.Stat_t
		if unix.Fstat(current, &ancestor) != nil || !protectedMode(ancestor, true) || !writeDenied(current) {
			return ErrUnavailable
		}
		if index == len(parts)-1 {
			break
		}
		next, err := unix.Openat(current, parts[index], unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return ErrUnavailable
		}
		_ = unix.Close(current)
		current = next
	}
	// Re-resolve only the leaf beneath the checked, pinned parent and compare
	// its identity with the file already hashed. No symlink may replace it.
	var leaf unix.Stat_t
	if unix.Fstatat(current, filepath.Base(file.path), &leaf, unix.AT_SYMLINK_NOFOLLOW) != nil || leaf.Dev != target.Dev || leaf.Ino != target.Ino || !protectedMode(leaf, false) {
		return ErrUnavailable
	}
	return nil
}

func protectedMode(info unix.Stat_t, directory bool) bool {
	kind := uint32(unix.S_IFREG)
	if directory {
		kind = unix.S_IFDIR
	}
	return info.Uid == 0 && info.Mode&unix.S_IFMT == kind && info.Mode&0022 == 0
}

func writeDenied(fd int) bool {
	// AT_EMPTY_PATH queries this exact inode, not a pathname that could be
	// replaced. Require an affirmative permission/read-only denial; unsupported
	// syscalls or other failures do not prove that the caller cannot write.
	err := unix.Faccessat2(fd, "", unix.W_OK, unix.AT_EMPTY_PATH|unix.AT_EACCESS)
	return errors.Is(err, unix.EACCES) || errors.Is(err, unix.EROFS) || errors.Is(err, unix.EPERM)
}
