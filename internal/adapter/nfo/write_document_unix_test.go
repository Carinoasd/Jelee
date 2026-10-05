//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceNFODocumentRejectsSymlinkTargetAndBackup(t *testing.T) {
	for _, name := range []string{"movie.nfo", "movie.nfo.jelee.bak"} {
		t.Run(name, func(t *testing.T) {
			path, dir, original, replacement := writeDocumentFixture(t)
			target := filepath.Join(path, "user-file")
			if err := os.WriteFile(target, original.original, 0600); err != nil {
				t.Fatal(err)
			}
			if name == "movie.nfo" {
				if err := dir.Remove(name); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, filepath.Join(path, name)); err != nil {
				t.Fatal(err)
			}
			if err := replaceNFODocument(context.Background(), dir, "movie.nfo", original, replacement, 1); err != ErrChanged && err != ErrReplace {
				t.Fatalf("symlink accepted: %v", err)
			}
			data, _ := os.ReadFile(target)
			if !bytes.Equal(data, original.original) {
				t.Fatal("symlink target changed")
			}
			checkNoNFOStages(t, path)
		})
	}
}

func TestReplaceNFODocumentRejectsReplacedLockSidecar(t *testing.T) {
	path, dir, original, replacement := writeDocumentFixture(t)
	ops := nativeNFOWriteOperations()
	base := ops
	changed := false
	ops.syncFile = func(file *os.File) error {
		if !changed {
			changed = true
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".lock") {
					if err := dir.Remove(entry.Name()); err != nil {
						return err
					}
					if err := dir.WriteFile(entry.Name(), nil, 0600); err != nil {
						return err
					}
				}
			}
		}
		return base.syncFile(file)
	}
	if err := replaceNFODocumentWithOperations(context.Background(), dir, "movie.nfo", original, replacement, 0, ops); err != ErrFileLock {
		t.Fatalf("replaced sidecar accepted: %v", err)
	}
	data, _ := dir.ReadFile("movie.nfo")
	if !bytes.Equal(data, original.original) {
		t.Fatal("source changed after losing lock identity")
	}
	checkNoNFOStages(t, path)
}
