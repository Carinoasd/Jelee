//go:build linux

package ignoresource

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyLinuxRejectsLinkedRule(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside")
		if !dangling {
			if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(outside, filepath.Join(root, ".ignore")); err != nil {
			t.Fatal(err)
		}
		parent, err := openNativeRoot(root)
		if err != nil {
			t.Fatal(err)
		}
		budget := 100
		stamp, data, readErr := readLegacyRule(context.Background(), parent, &budget)
		closeErr := parent.Close()
		if readErr != ErrUnsafe || closeErr != nil || stamp.present || len(data) != 0 || budget != 100 {
			t.Fatal("link followed or absence fabricated", readErr)
		}
	}
}
