package app

import (
	"context"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/testutil"
)

// A run that returns at the stop, inside a long grace, ends the grace
// waiter with it: RunBackground leaves no goroutine timing the grace
// (G29.3).
func TestRunBackgroundStopInsideGraceLeavesNoGoroutines(t *testing.T) {
	testutil.TrackGoroutines(t)
	for _, idle := range []bool{true, false} {
		started := make(chan struct{}, 1)
		task := BackgroundTask{Kind: domain.JobPlaybackProgressFlush, Interval: time.Millisecond, IdleWait: idle, StopGrace: time.Hour,
			Run: func(stop, _ context.Context) (bool, error) {
				select {
				case started <- struct{}{}:
				default:
				}
				<-stop.Done()
				return false, nil
			}}
		if err := task.Validate(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); RunBackground(ctx, task, nil) }()
		<-started
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the run did not stop")
		}
	}
}
