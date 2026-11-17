//go:build windows

package subtitles

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Windows cannot prove a cache root private, so the derived cache stays off
// there like Matroska extraction, even over a clean existing root.
func TestDerivedCacheOffOnWindows(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDerivedCache(root, 0); !errors.Is(err, ErrDerivedUnavailable) {
		t.Fatalf("derived cache on Windows: %v", err)
	}
}
