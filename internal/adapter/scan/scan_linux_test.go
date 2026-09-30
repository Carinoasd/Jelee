//go:build linux

package scan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestLinuxInvalidNamesAreCountedWithoutNormalization(t *testing.T) {
	for label, name := range map[string]string{"invalid-utf8": "bad\xff", "backslash": "back\\slash", "colon": "stream:ads"} {
		t.Run(label, func(t *testing.T) {
			root := t.TempDir()
			writeScanFile(t, root, name, []byte("not renamed"))
			observed, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(observed) != 1 || observed[0].Name() != name {
				t.Skip("filesystem changed the requested filename bytes; this boundary cannot be exercised here")
			}
			writeScanFile(t, root, "valid 名称.mp4", []byte("unchanged"))
			entries, _, skipped := collect(t, root, ".")
			if len(entries) != 1 || entries[0].Path != "valid 名称.mp4" || skipped != 1 {
				t.Fatalf("unrepresentable name was not skipped: entries=%#v skipped=%d", entries, skipped)
			}
		})
	}
}

func TestLinuxFIFOIsSkippedWithoutOpeningIt(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "private-pipe"), 0600); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("filesystem does not support FIFO creation: %v", err)
		}
		t.Fatal(err)
	}
	entries, directories, skipped := collect(t, root, ".")
	if len(entries) != 0 || len(directories) != 0 || skipped != 1 {
		t.Fatal("FIFO was emitted as media or directory")
	}
	if err := ValidateRoot(context.Background(), filepath.Join(root, "private-pipe")); err != domain.ErrScanUnavailable {
		t.Fatal("FIFO accepted as root")
	}
}

func TestLinuxExternalSymlinkAncestorReplacementCannotEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeScanFile(t, root, "pivot/child/inside.mp4", []byte("inside root"))
	writeScanFile(t, outside, "child/outside-secret.mp4", []byte("outside root"))
	probe := filepath.Join(root, "link-probe")
	if err := os.Symlink(outside, probe); err != nil {
		t.Skipf("filesystem cannot create symlinks: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	pivot, parked := filepath.Join(root, "pivot"), filepath.Join(root, "parked")
	var stop atomic.Bool
	var exchanges atomic.Int64
	attackerDone := make(chan error, 1)
	go func() {
		for !stop.Load() {
			if err := os.Rename(pivot, parked); err != nil {
				if errors.Is(err, fs.ErrPermission) {
					// DrvFS can deny directory rename while a Windows-backed
					// handle is open. Retry rather than call it a scanner flaw.
					time.Sleep(time.Millisecond)
					continue
				}
				attackerDone <- err
				return
			}
			if err := os.Symlink(outside, pivot); err != nil {
				_ = os.Rename(parked, pivot)
				attackerDone <- err
				return
			}
			if err := os.Remove(pivot); err != nil {
				attackerDone <- err
				return
			}
			var restoreErr error
			for retry := 0; retry < 1000; retry++ {
				restoreErr = os.Rename(parked, pivot)
				if !errors.Is(restoreErr, fs.ErrPermission) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if restoreErr != nil {
				attackerDone <- restoreErr
				return
			}
			exchanges.Add(1)
		}
		attackerDone <- nil
	}()
	defer func() {
		stop.Store(true)
		if err := <-attackerDone; err != nil {
			t.Fatalf("race setup failed: %v", err)
		}
		if exchanges.Load() == 0 {
			t.Skip("filesystem prevented all directory/symlink swaps while scanning; race not exercised")
		}
		t.Logf("completed %d directory/symlink replacement cycles", exchanges.Load())
	}()
	for iteration := 0; iteration < 300; iteration++ {
		err := New().ScanDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "pivot/child"}, func(batch domain.ScanBatch) error {
			for _, entry := range batch.Entries {
				if entry.Path != "pivot/child/inside.mp4" || entry.Size != int64(len("inside root")) {
					t.Fatalf("metadata from outside root escaped: %q", entry.Path)
				}
			}
			return nil
		})
		if err != nil && err != domain.ErrScanUnavailable && err != domain.ErrScanIO {
			t.Fatalf("unexpected error from raced lookup: %v", err)
		}
	}
}

func TestLinuxUnreadableRegularContentStillProvidesMetadata(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "content-not-readable.mp4")
	writeScanFile(t, root, filepath.Base(name), []byte("original content"))
	if err := os.Chmod(name, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(name, 0600)
	if _, err := os.ReadFile(name); !errors.Is(err, fs.ErrPermission) {
		t.Skip("filesystem or current identity does not enforce unreadable mode; cannot prove metadata-only access by permissions")
	}
	entries, _, skipped := collect(t, root, ".")
	if len(entries) != 1 || entries[0].Size != int64(len("original content")) || skipped != 0 {
		t.Fatal("regular-file metadata unnecessarily required content access")
	}
}
