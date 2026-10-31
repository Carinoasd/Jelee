//go:build windows

package runtime

import (
	"io"
	"log/slog"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/media"
)

// Windows cannot prove the cache root private, so no UTF-8 copies are
// produced there and the cleanup job has no cache below the root to sweep.
func TestUTF8ServiceOffOnWindows(t *testing.T) {
	c := utf8TestConfig(t)
	service := newUTF8Service(c, &utf8Tracks{err: media.ErrNotFound}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if service.Available() {
		t.Fatal("derived cache on Windows")
	}
	if sweeper := prepareSubtitleCacheSweeper(c); sweeper != nil {
		t.Fatalf("sweeper on Windows: %T", sweeper)
	}
}
