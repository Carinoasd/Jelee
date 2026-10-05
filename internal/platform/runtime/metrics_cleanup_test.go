package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
)

func TestMetricsCleanupRetainsPoolUntilSnapshotJoined(t *testing.T) {
	l := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls, closed atomic.Int32
	l.closeStore = func() { closed.Add(1) }
	l.closeTelemetry = func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			return context.DeadlineExceeded
		}
		if _, bounded := ctx.Deadline(); bounded || ctx.Err() != nil {
			t.Error("cleanup ownership must survive caller timeout")
		}
		close(entered)
		<-release
		return nil
	}
	go func() { l.closePool(); close(done) }()
	waitChannel(t, entered)
	if closed.Load() != 0 {
		t.Error("pool closed while metrics still had an active snapshot")
	}
	close(release)
	waitChannel(t, done)
	l.closePool()
	if closed.Load() != 1 || calls.Load() != 2 || l.stopErr != nil {
		t.Fatal("eventual metrics cleanup did not release the pool exactly once")
	}
}

func TestMetricsCleanupFailureKeepsPoolOwned(t *testing.T) {
	l := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
	var closed bool
	l.closeStore = func() { closed = true }
	l.closeTelemetry = func(context.Context) error { return errors.New("private exporter failure") }
	l.closePool()
	if closed || l.stopErr == nil || l.stopErr.Error() != "metrics shutdown failed" {
		t.Fatal("failed metrics cleanup closed the pool or leaked error details")
	}
}
