//go:build !linux && !windows

package scan

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"os"
	"time"
)

type directoryWatchBackend struct{}

func newDirectoryWatchBackend() (*directoryWatchBackend, error) {
	return nil, domain.ErrScanUnavailable
}
func (*directoryWatchBackend) Add(*os.File) error { return domain.ErrScanUnavailable }
func (*directoryWatchBackend) Poll(context.Context, time.Duration) (bool, bool, error) {
	return false, false, domain.ErrScanUnavailable
}
func (*directoryWatchBackend) Close() error { return nil }
