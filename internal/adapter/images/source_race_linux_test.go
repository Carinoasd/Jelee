//go:build linux

package images

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// The listing handle must never be closed while ReadDir is filling a page:
// os.File keeps directory read state outside its descriptor lock, so a close
// from the cancellation callback would be a data race under -race.
func TestImageSourceCancelDuringLargeDirectoryListingDoesNotRace(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	parent := filepath.Join(source.RootPath, "movie")
	imageWrite(t, filepath.Join(parent, "poster.jpg"), "image")
	for i := 0; i < imageDirectoryEntries-1000; i++ {
		imageWrite(t, filepath.Join(parent, fmt.Sprintf("entry-%05d", i)), "")
	}
	for i := 0; i < 150; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Duration(i%30)*100*time.Microsecond, cancel)
		value, err := stageLocalPrimary(ctx, source, scratch, 100)
		timer.Stop()
		cancel()
		if value != nil {
			_ = value.Close()
		}
		_ = err
	}
	imageScratchEmpty(t, scratch)
}
