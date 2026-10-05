//go:build linux || windows

package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func TestDirectoryWatchActualChangesAndCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	watcher, err := NewDirectoryWatcher(WatchOptions{MaxDirectories: 16, QuietPeriod: 100 * time.Millisecond, MaxDelay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan struct{}, 16)
	done := make(chan error, 1)
	go func() {
		done <- watcher.Observe(ctx, []domain.ScanDirectory{{RootID: "11111111-1111-4111-8111-111111111111", RootPath: root, Path: "."}}, func(context.Context) error {
			select {
			case events <- struct{}{}:
			default:
			}
			return nil
		})
	}()
	wait := func() {
		t.Helper()
		select {
		case <-events:
		case err := <-done:
			t.Fatal("watch stopped", err)
		case <-time.After(8 * time.Second):
			t.Fatal("watch notification missing")
		}
	}
	wait() // All initial watches are armed before this startup dirty signal.
	if err := os.WriteFile(filepath.Join(root, "child", "film.mkv"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	wait()
	if err := os.Mkdir(filepath.Join(root, "new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new", "added.mkv"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	wait()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("watch cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch resources did not stop")
	}
	data, err := os.ReadFile(filepath.Join(root, "child", "film.mkv"))
	if err != nil || string(data) != "original" {
		t.Fatal("watch changed original", err)
	}
}

func TestDirectoryWatchLimitAndCallbackFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	roots := []domain.ScanDirectory{{RootID: "11111111-1111-4111-8111-111111111111", RootPath: root, Path: "."}}
	w, _ := NewDirectoryWatcher(WatchOptions{MaxDirectories: 1, QuietPeriod: time.Second, MaxDelay: time.Second})
	if err := w.Observe(context.Background(), roots, func(context.Context) error { t.Fatal("over-limit watcher became ready"); return nil }); !errors.Is(err, domain.ErrScanLimit) {
		t.Fatal("directory bound", err)
	}
	w, _ = NewDirectoryWatcher(DefaultWatchOptions())
	failure := errors.New("callback failure")
	if err := w.Observe(context.Background(), roots, func(context.Context) error { return failure }); !errors.Is(err, failure) {
		t.Fatal("callback error swallowed", err)
	}
}

func TestDirectoryWatchPinnedObjectAfterRename(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original")
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := openDirectory(context.Background(), original, ".")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	moved := filepath.Join(root, "moved")
	if err = os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	backend, err := newDirectoryWatchBackend()
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	if err = backend.Add(file); err != nil {
		t.Fatal("pinned registration", err)
	}
	runtime.GC()
	if err = os.WriteFile(filepath.Join(moved, "inside.mkv"), []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		changed, _, err := backend.Poll(ctx, 100*time.Millisecond)
		if err != nil {
			t.Fatal("reopened replacement instead of pinned directory", err)
		}
		if changed {
			break
		}
	}
}

func TestDirectoryWatchRebuildIncludesNewChildren(t *testing.T) {
	root := t.TempDir()
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	recorded := &watchTestBudget{budget: budget}
	w, err := NewDirectoryWatcher(WatchOptions{Budget: recorded, MaxDirectories: 16, QuietPeriod: 100 * time.Millisecond, MaxDelay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 32)
	done := make(chan error, 1)
	go func() {
		done <- w.Observe(ctx, []domain.ScanDirectory{{RootID: watchTestRootID, RootPath: root, Path: "."}}, func(context.Context) error {
			if budget.Stats() != (resources.Stats{}) {
				t.Error("idle observer held shared I/O permit")
			}
			select {
			case events <- struct{}{}:
			default:
			}
			return nil
		})
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("reconciled watcher did not stop")
		}
	}()
	wait := func() {
		t.Helper()
		select {
		case <-events:
		case <-time.After(5 * time.Second):
			t.Fatal("child notification missing")
		}
	}
	wait()
	child := filepath.Join(root, "new")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	// Structural events rebuild within two seconds. Write after that rebuild,
	// so a notification for merely creating the directory cannot pass the test.
	timer := time.NewTimer(2500 * time.Millisecond)
	<-timer.C
drain:
	for {
		select {
		case <-events:
		default:
			break drain
		}
	}
	if recorded.calls.Load() < 2 {
		t.Fatal("rebuild did not reacquire I/O")
	}
	if err := os.WriteFile(filepath.Join(child, "later.mkv"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	wait()
}

const watchTestRootID = "11111111-1111-4111-8111-111111111111"
