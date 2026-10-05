//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package nfo

import "os"

func regularNFOFile(info os.FileInfo) bool { return false }
func syncNFODirectory(root *os.Root) error { return ErrReplace }
