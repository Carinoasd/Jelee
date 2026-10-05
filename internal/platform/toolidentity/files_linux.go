package toolidentity

import (
	"errors"
	"os"
	"syscall"
)

func readFlags() int { return os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK }
func safeFileInfo(info os.FileInfo, directory bool) bool {
	if info == nil {
		return false
	}
	if directory {
		return info.Mode().Type() == os.ModeDir
	}
	return info.Mode().IsRegular()
}
func makePrivate(path string) error {
	if err := os.Chmod(path, 0700); err != nil { //nolint:gosec // G302: a private executable must keep its owner execute bit
		return err
	}
	info, err := os.Lstat(path) //nolint:gosec // G703: path is the project-local tool file being verified
	if err != nil {
		return err
	}
	if !safeFileInfo(info, true) || info.Mode().Perm() != 0700 {
		return errors.New("identity_private_directory_unavailable")
	}
	return nil
}
