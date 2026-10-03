package runtime

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type blockedScheduleDispatcher struct {
	entered chan struct{}
	exited  chan struct{}
}

func (d *blockedScheduleDispatcher) DispatchSchedule(ctx context.Context) (bool, error) {
	close(d.entered)
	<-ctx.Done()
	close(d.exited)
	return false, ctx.Err()
}

func TestSchedulerStopsBlockedDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := newTestWorker()
	close(inner.release)
	d := &blockedScheduleDispatcher{entered: make(chan struct{}), exited: make(chan struct{})}
	w := &scheduledWorker{worker: inner, dispatch: d, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitChannel(t, d.entered)
	cancel()
	stop, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := w.Stop(stop); err != nil {
		t.Fatal(err)
	}
	waitChannel(t, d.exited)
	if err := w.Start(ctx); err == nil {
		t.Fatal("restarted single-use lifecycle")
	}
}
