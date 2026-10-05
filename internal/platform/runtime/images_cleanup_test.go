package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
)

func TestImagesCleanupRetainsPoolUntilResponsesJoined(t *testing.T) {
	life := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls, closed atomic.Int32
	life.closeImages = func(ctx context.Context) error {
		calls.Add(1)
		if ctx.Err() != nil {
			t.Error("cleanup ownership must survive request cancellation")
		}
		close(entered)
		<-release
		return nil
	}
	life.closeStore = func() { closed.Add(1) }
	life.cancel()
	go func() { life.closePool(); close(done) }()
	waitChannel(t, entered)
	if closed.Load() != 0 {
		t.Error("pool closed before image response joined")
	}
	close(release)
	waitChannel(t, done)
	life.closePool()
	if closed.Load() != 1 || calls.Load() != 1 || life.stopErr != nil {
		t.Fatal("image cleanup was not exactly once")
	}
}

func TestImagesCleanupFailureKeepsPoolOwned(t *testing.T) {
	life := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
	closed := false
	life.closeImages = func(context.Context) error { return errors.New("private image failure") }
	life.closeStore = func() { closed = true }
	life.closePool()
	if closed || life.stopErr == nil || life.stopErr.Error() != "image shutdown failed" {
		t.Fatal("unsafe image cleanup failure")
	}
}
