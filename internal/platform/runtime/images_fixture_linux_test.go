package runtime

import (
	"os"
	"syscall"
	"testing"
)

func imagesIntegrationScratch(t *testing.T) string {
	t.Helper()
	// Use native Linux storage even when the toolchain's TMPDIR is a mounted
	// Windows build cache. MkdirTemp creates a directory owned by this test.
	directory, err := os.MkdirTemp("/tmp", "jelee-images-runtime-")
	if err != nil {
		t.Fatal("create owned image integration scratch")
	}
	t.Cleanup(func() {
		if os.RemoveAll(directory) != nil {
			t.Error("remove owned image integration scratch")
		}
	})
	if os.Chmod(directory, 0700) != nil {
		t.Fatal("make owned image integration scratch private")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("image integration scratch permissions differ")
	}
	state, ok := info.Sys().(*syscall.Stat_t)
	if !ok || state.Uid != uint32(os.Geteuid()) {
		t.Fatal("image integration scratch owner differs")
	}
	return directory
}
