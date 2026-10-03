//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import "os"

func regularNFOFile(info os.FileInfo) bool { return validSourceInfo(info) }
func syncNFODirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}
