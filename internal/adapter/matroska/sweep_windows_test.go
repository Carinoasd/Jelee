//go:build windows

package matroska

import (
	"errors"
	"testing"
)

// Windows cannot prove a cache root private, so cleanup stays unavailable
// there like extraction itself.
func TestSweeperUnavailableOnWindows(t *testing.T) {
	if _, err := NewSweeper(Config{CacheRoot: t.TempDir()}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("sweeper on Windows", err)
	}
}
