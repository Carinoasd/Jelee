//go:build linux

package ignoresource

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

// Cancelling a scan must not close the directory handle while a page is being
// read: os.File keeps directory read state outside the descriptor lock.
func TestNativeScanCancelDuringListingDoesNotRace(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	for i := 0; i < 8000; i++ {
		writeRule(t, root, fmt.Sprintf("entry-%05d.mkv", i), "")
	}
	for i := 0; i < 150; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Duration(i%30)*100*time.Microsecond, cancel)
		_ = NewResolver().ScanDirectory(ctx, root, ".", ignore.Options{}, func(ScanBatch) error { return nil })
		timer.Stop()
		cancel()
	}
}
