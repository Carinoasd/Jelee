//go:build linux

package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func raceFixtureDirectory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < 8000; i++ {
		writeScanFile(t, root, fmt.Sprintf("entry-%05d.mkv", i), nil)
	}
	return root
}

// A cancellation closing the directory handle while ReadDir fills a page is a
// data race: os.File keeps directory read state outside the descriptor lock.
func TestScanDirectoryCancelDuringListingDoesNotRace(t *testing.T) {
	root := raceFixtureDirectory(t)
	for i := 0; i < 150; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Duration(i%30)*100*time.Microsecond, cancel)
		_ = New().ScanDirectory(ctx, domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, func(domain.ScanBatch) error { return nil })
		timer.Stop()
		cancel()
	}
}

func TestWatchDirectoryEnumerationCancelDuringListingDoesNotRace(t *testing.T) {
	root := raceFixtureDirectory(t)
	for i := 0; i < 150; i++ {
		file, err := os.Open(filepath.Clean(root))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Duration(i%30)*100*time.Microsecond, cancel)
		var queue []domain.ScanDirectory
		_ = appendWatchDirectories(ctx, file, domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, &queue, 100)
		timer.Stop()
		cancel()
		_ = file.Close()
	}
}
