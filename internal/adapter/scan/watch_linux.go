//go:build linux

package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

type directoryWatchBackend struct{ watcher *fsnotify.Watcher }

func newDirectoryWatchBackend() (*directoryWatchBackend, error) {
	w, err := fsnotify.NewBufferedWatcher(256)
	if err != nil {
		return nil, domain.ErrScanUnavailable
	}
	return &directoryWatchBackend{watcher: w}, nil
}

// The caller keeps the descriptor alive until Close. The proc descriptor link
// addresses the already-open directory; media paths are never resolved again.
func (b *directoryWatchBackend) Add(file *os.File) error {
	var stat unix.Statfs_t
	if unix.Fstatfs(int(file.Fd()), &stat) != nil {
		return domain.ErrScanUnavailable
	}
	switch uint32(stat.Type) {
	case unix.NFS_SUPER_MAGIC, unix.V9FS_MAGIC, unix.FUSE_SUPER_MAGIC, 0xff534d42, 0xfe534d42, 0x517b:
		return domain.ErrScanUnavailable
	}

	if err := b.watcher.Add(fmt.Sprintf("/proc/self/fd/%d", file.Fd())); err != nil {
		return domain.ErrScanUnavailable
	}
	return nil
}

func (b *directoryWatchBackend) Poll(ctx context.Context, wait time.Duration) (bool, bool, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, false, ctx.Err()
	case <-timer.C:
		return false, false, nil
	case event, ok := <-b.watcher.Events:
		if !ok {
			return false, false, domain.ErrScanUnavailable
		}
		changed := event.Has(fsnotify.Write | fsnotify.Create | fsnotify.Remove | fsnotify.Rename | fsnotify.Chmod)
		return changed, event.Has(fsnotify.Create | fsnotify.Remove | fsnotify.Rename), nil
	case err, ok := <-b.watcher.Errors:
		if ok && errors.Is(err, fsnotify.ErrEventOverflow) {
			return true, true, nil
		}
		return false, false, domain.ErrScanUnavailable
	}
}

func (b *directoryWatchBackend) Close() error {
	if err := b.watcher.Close(); err != nil {
		return domain.ErrScanUnavailable
	}
	return nil
}
