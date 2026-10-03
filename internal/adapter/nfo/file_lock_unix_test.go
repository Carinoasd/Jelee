//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestNFOFileLockRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	path := t.TempDir()
	dir := lockTestRoot(t, path)
	digest := sha256.Sum256([]byte(lockNameKey("movie.nfo")))
	sidecar := filepath.Join(path, ".jelee-nfo-"+hex.EncodeToString(digest[:])+".lock")
	target := filepath.Join(path, "user-file")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, sidecar); err != nil {
		t.Fatal(err)
	}
	if _, err := lockNFOFile(context.Background(), dir, "movie.nfo"); err != ErrFileLock {
		t.Fatal("symlink sidecar accepted")
	}
	info, err := os.Stat(target)
	if err != nil || info.Size() != 0 {
		t.Fatal("symlink target changed")
	}
}
