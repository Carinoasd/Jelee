//go:build linux || windows

package scan

import (
	"context"
	"errors"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type watchTestBudget struct {
	budget    *resources.Budget
	calls     atomic.Int32
	attempted chan struct{}
}

func (b *watchTestBudget) Acquire(ctx context.Context, class app.WorkClass) (func(), error) {
	b.calls.Add(1)
	if b.attempted != nil {
		select {
		case b.attempted <- struct{}{}:
		default:
		}
	}
	if class != app.WorkIO {
		return nil, domain.ErrInvalid
	}
	return b.budget.Acquire(ctx, class)
}
func TestWatchBudgetWaitCancellationAndBuildFailure(t *testing.T) {
	for _, queue := range []int{0, 1} {
		b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: queue})
		held, err := b.Acquire(context.Background(), app.WorkCPU)
		if err != nil {
			t.Fatal(err)
		}
		recorder := &watchTestBudget{budget: b, attempted: make(chan struct{}, 2)}
		opts := DefaultWatchOptions()
		opts.Budget = recorder
		w, err := NewDirectoryWatcher(opts)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			tree, err := w.build(ctx, []domain.ScanDirectory{{RootPath: "invalid", Path: "."}})
			if tree != nil {
				tree.close()
			}
			done <- err
		}()
		attempts := 1
		if queue == 0 {
			attempts = 2
		}
		for i := 0; i < attempts; i++ {
			select {
			case <-recorder.attempted:
			case <-time.After(3 * time.Second):
				t.Fatal("watch did not retry/wait")
			}
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal("waiting watch touched invalid path", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("watch cancellation stalled")
		}
		held()
		if b.Stats() != (resources.Stats{}) {
			t.Fatal("watch wait leaked permit")
		}
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	opts := DefaultWatchOptions()
	opts.Budget = b
	opts.MaxDirectories = 1
	w, _ := NewDirectoryWatcher(opts)
	tree, err := w.build(context.Background(), []domain.ScanDirectory{{RootID: watchTestRootID, RootPath: root, Path: "."}})
	if tree != nil {
		tree.close()
		t.Fatal("limited build returned watch")
	}
	if !errors.Is(err, domain.ErrScanLimit) || b.Stats() != (resources.Stats{}) {
		t.Fatal("failed build lost error or leaked permit", err)
	}
}
