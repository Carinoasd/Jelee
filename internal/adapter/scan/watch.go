package scan

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type WatchOptions struct {
	Budget         app.WorkBudget
	MaxDirectories int
	QuietPeriod    time.Duration
	MaxDelay       time.Duration
}

func DefaultWatchOptions() WatchOptions {
	return WatchOptions{MaxDirectories: 1024, QuietPeriod: time.Second, MaxDelay: 5 * time.Second}
}

type DirectoryWatcher struct{ options WatchOptions }

func NewDirectoryWatcher(options WatchOptions) (*DirectoryWatcher, error) {
	if options.MaxDirectories < 1 || options.MaxDirectories > 4096 || options.QuietPeriod < 100*time.Millisecond || options.QuietPeriod > 5*time.Second || options.MaxDelay < options.QuietPeriod || options.MaxDelay > 30*time.Second {
		return nil, domain.ErrInvalid
	}
	return &DirectoryWatcher{options: options}, nil
}

type watchedTree struct {
	backend *directoryWatchBackend
	files   []*os.File
	roots   map[string]os.FileInfo
}

func (tree *watchedTree) close() error {
	err := tree.backend.Close()
	for _, file := range tree.files {
		_ = file.Close()
	}
	return err
}

func (w *DirectoryWatcher) build(ctx context.Context, roots []domain.ScanDirectory) (*watchedTree, error) {
	release, err := w.acquireIO(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	backend, err := newDirectoryWatchBackend()
	if err != nil {
		return nil, err
	}
	tree := &watchedTree{backend: backend, roots: make(map[string]os.FileInfo)}
	ready := false
	defer func() {
		if !ready {
			_ = tree.close()
		}
	}()
	queue := append([]domain.ScanDirectory(nil), roots...)
	for offset := 0; offset < len(queue); offset++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := queue[offset]
		file, err := openDirectory(ctx, dir.RootPath, dir.Path)
		if err != nil {
			return nil, err
		}
		tree.files = append(tree.files, file)
		if dir.Path == "." {
			info, err := file.Stat()
			if err != nil {
				return nil, domain.ErrScanUnavailable
			}
			tree.roots[dir.RootPath] = info
		}
		if err = backend.Add(file); err != nil {
			return nil, err
		}
		if err = appendWatchDirectories(ctx, file, dir, &queue, w.options.MaxDirectories); err != nil {
			return nil, err
		}
	}
	if err = tree.checkRoots(); err != nil {
		return nil, err
	}
	ready = true
	return tree, nil
}

func appendWatchDirectories(ctx context.Context, file *os.File, parent domain.ScanDirectory, queue *[]domain.ScanDirectory, limit int) error {
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); _ = file.Close() })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		children, err := file.ReadDir(domain.ScanBatchMaxEntries)
		if err != nil && !errors.Is(err, io.EOF) {
			return scanError(ctx, domain.ErrScanIO)
		}
		for _, child := range children {
			// Reparse points and symbolic links are never traversed. No event
			// name is opened; only bounded directory entries from this handle.
			info, infoErr := child.Info()
			if infoErr != nil {
				return scanError(ctx, domain.ErrScanIO)
			}
			if info.Mode().Type() != os.ModeDir || !validChildName(child.Name()) {
				continue
			}
			if len(*queue) >= limit {
				return domain.ErrScanLimit
			}
			relative := child.Name()
			if parent.Path != "." {
				relative = parent.Path + "/" + relative
			}
			if err := validRelative(relative); err != nil {
				return err
			}
			*queue = append(*queue, domain.ScanDirectory{RootID: parent.RootID, RootPath: parent.RootPath, Path: relative})
		}
		if errors.Is(err, io.EOF) {
			return ctx.Err()
		}
	}
}

func (tree *watchedTree) checkRoots() error {
	for path, before := range tree.roots {
		after, err := os.Lstat(path)
		if err != nil || after.Mode().Type() != os.ModeDir || !os.SameFile(before, after) {
			return domain.ErrScanUnavailable
		}
	}
	return nil
}

// Observe emits only a dirty signal. Startup and structural reconciliation
// require a full safe scan, covering events missed while watches were rebuilt.
// The callback must be bounded and cancellable; its failure ends observation
// so a future owner restarts with a fresh dirty signal.
func (w *DirectoryWatcher) Observe(ctx context.Context, roots []domain.ScanDirectory, changed func(context.Context) error) (result error) {
	if ctx == nil || changed == nil || len(roots) < 1 || len(roots) > 32 || len(roots) > w.options.MaxDirectories {
		return domain.ErrInvalid
	}
	for _, root := range roots {
		if !domain.ValidID(root.RootID) || root.Path != "." {
			return domain.ErrInvalid
		}
	}
	tree, err := w.build(ctx, roots)
	if err != nil {
		return err
	}
	defer func() {
		if tree != nil {
			result = errors.Join(result, tree.close())
		}
	}()
	if err = changed(ctx); err != nil {
		return err
	}
	var first, last, rebuild time.Time
	health := time.Now().Add(5 * time.Second)
	for {
		dirty, structure, err := tree.backend.Poll(ctx, 100*time.Millisecond)
		if err != nil {
			return err
		}
		now := time.Now()
		if dirty {
			if first.IsZero() {
				first = now
			}
			last = now
		}
		if structure && rebuild.IsZero() {
			rebuild = now.Add(2 * time.Second)
		}
		if !rebuild.IsZero() && !now.Before(rebuild) {
			old := tree
			tree = nil
			if err = old.close(); err != nil {
				return err
			}
			tree, err = w.build(ctx, roots)
			if err != nil {
				return err
			}
			if err = changed(ctx); err != nil {
				return err
			}
			first, last, rebuild = time.Time{}, time.Time{}, time.Time{}
		} else if !first.IsZero() && (now.Sub(last) >= w.options.QuietPeriod || now.Sub(first) >= w.options.MaxDelay) {
			if err = changed(ctx); err != nil {
				return err
			}
			first, last = time.Time{}, time.Time{}
		}
		if !now.Before(health) {
			if err = w.checkRoots(ctx, tree); err != nil {
				return err
			}
			health = now.Add(5 * time.Second)
		}
	}
}

// Only active traversal/checks occupy a shared slot; native event polling does
// not hold a permit for the watch's lifetime. At most the bounded watch runners
// wait here, and cancellation interrupts both queueing and overflow backoff.
func (w *DirectoryWatcher) acquireIO(ctx context.Context) (func(), error) {
	if w.options.Budget == nil {
		return func() {}, ctx.Err()
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, err := w.options.Budget.Acquire(ctx, app.WorkIO)
		if !errors.Is(err, domain.ErrResourceBusy) {
			return release, err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (w *DirectoryWatcher) checkRoots(ctx context.Context, tree *watchedTree) error {
	release, err := w.acquireIO(ctx)
	if err != nil {
		return err
	}
	defer release()
	return tree.checkRoots()
}
