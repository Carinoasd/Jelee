// runtime-image verifies the exact non-executable staging tree during image build.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MoYuanCN/Jelee/tools"
)

var errInvalid = errors.New("runtime_image_invalid")

type pin struct {
	digest  string
	maximum int64
}

func imagePins() (map[string]pin, error) {
	probe, err := tools.FFprobeSpec("linux-amd64")
	if err != nil || len(probe.Licenses) != 1 {
		return nil, errInvalid
	}
	runtime, err := tools.RuntimeSpec("linux-amd64")
	if err != nil {
		return nil, errInvalid
	}
	pins := map[string]pin{
		filepath.FromSlash("usr/lib/jelee/ffprobe"):        {probe.SHA256, 512 << 20},
		filepath.FromSlash("licenses/ffprobe/LICENSE.txt"): {probe.Licenses[0].SHA256, 4 << 20},
	}
	for _, library := range runtime.Libraries {
		pins[filepath.FromSlash(library.ContainerPath[1:])] = pin{library.SHA256, 8 << 20}
	}
	for _, license := range runtime.Licenses {
		pins[filepath.FromSlash(license.ContainerPath[1:])] = pin{license.SHA256, 4 << 20}
	}
	return pins, nil
}

func verify(root string, pins map[string]pin) error {
	return verifyWithOpen(root, pins, os.Open)
}

func verifyWithOpen(root string, pins map[string]pin, open func(string) (*os.File, error)) error {
	if len(pins) == 0 || len(pins) > 32 {
		return errInvalid
	}
	directories := map[string]bool{".": true}
	for name, expected := range pins {
		decoded, err := hex.DecodeString(expected.digest)
		if name == "." || !filepath.IsLocal(name) || filepath.Clean(name) != name || !fs.ValidPath(filepath.ToSlash(name)) || strings.ContainsAny(filepath.ToSlash(name), ":\\") || expected.maximum < 1 || expected.maximum > 512<<20 || err != nil || len(decoded) != 32 || strings.ToLower(expected.digest) != expected.digest {
			return errInvalid
		}
		for directory := filepath.Dir(name); directory != "."; directory = filepath.Dir(directory) {
			directories[directory] = true
		}
	}
	seen := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errInvalid
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errInvalid
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return errInvalid
		}
		if entry.IsDir() {
			if !directories[relative] {
				return errInvalid
			}
			return nil
		}
		expected, ok := pins[relative]
		info, err := entry.Info()
		if !ok || err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > expected.maximum {
			return errInvalid
		}
		file, err := open(path)
		if err != nil {
			return errInvalid
		}
		actual, err := file.Stat()
		if err != nil || !os.SameFile(info, actual) {
			file.Close()
			return errInvalid
		}
		hash := sha256.New()
		count, copyErr := io.Copy(hash, io.LimitReader(file, expected.maximum+1))
		after, statErr := file.Stat()
		current, pathErr := os.Lstat(path)
		closeErr := file.Close()
		if copyErr != nil || statErr != nil || pathErr != nil || closeErr != nil || !os.SameFile(info, current) || current.Mode()&os.ModeSymlink != 0 || count != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || hex.EncodeToString(hash.Sum(nil)) != expected.digest {
			return errInvalid
		}
		seen[relative] = true
		return nil
	})
	if err != nil || len(seen) != len(pins) {
		return errInvalid
	}
	return nil
}

func main() {
	pins, err := imagePins()
	if err != nil || len(os.Args) != 1 || verify("/runtime", pins) != nil {
		fmt.Fprintln(os.Stderr, "runtime_image_invalid")
		os.Exit(1)
	}
	fmt.Println("runtime image: exact manifest hashes verified; no media program executed")
}
