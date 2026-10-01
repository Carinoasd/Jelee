//go:build windows

package ignoresource

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLegacyWindowsRejectsReparseRule(t *testing.T) {
	root := t.TempDir()
	sourceJunction(t, filepath.Join(root, ".ignore"), t.TempDir())
	parent, err := openNativeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	budget := 100
	stamp, data, err := readLegacyRule(context.Background(), parent, &budget)
	if err != ErrUnsafe || stamp.present || len(data) != 0 || budget != 100 {
		t.Fatal("reparse source accepted", err)
	}
}
