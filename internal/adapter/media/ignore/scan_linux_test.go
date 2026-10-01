//go:build linux

package ignoresource

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

func TestNativeScanSpecialFilesAreSkippedWithoutFollowing(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	outside := filepath.Clean(t.TempDir())
	writeRule(t, outside, "secret.mkv", "secret")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "absent"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	writeRule(t, root, "kept.mkv", "data")
	var count int
	var skipped int64
	err := NewResolver().ScanDirectory(context.Background(), root, ".", ignore.Options{}, func(b ScanBatch) error {
		for _, e := range b.Entries {
			if e.Path != "kept.mkv" {
				t.Fatal("unsafe entry emitted", e)
			}
			count++
		}
		if b.Done {
			skipped = b.Skipped
		}
		return nil
	})
	if err != nil || count != 1 || skipped != 3 {
		t.Fatal("special file scan", count, skipped, err)
	}
	d, err := openNativeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, name := range []string{"linked", "dangling", "fifo"} {
		child, absent, err := d.(scanDirectory).OpenDirectoryOrAbsent(name)
		if err != ErrUnsafe || absent || child != nil {
			t.Fatal("unsafe child represented as absent", name, err)
		}
	}
}
