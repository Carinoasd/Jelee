package jobs

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestRunnerLocalCancellationInterruptsWithoutHeartbeat(t *testing.T) {
	f := oneJob(t)
	entered := make(chan struct{})
	opts := DefaultOptions()
	opts.Workers = 1
	opts.Clock = newTestClock()
	r, err := New(f.repo, scannerFunc(func(ctx context.Context, _ domain.ScanDirectory, _ func(domain.ScanBatch) error) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer r.Stop(context.Background())
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("scanner did not start")
	}
	r.NotifyJobCancellation("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	select {
	case <-f.terminal:
		t.Fatal("unrelated ID cancelled work")
	default:
	}
	r.NotifyJobCancellation("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	select {
	case terminal := <-f.terminal:
		if terminal.state != domain.JobCancelled || terminal.code != "" {
			t.Fatal("local notification did not cancel", terminal)
		}
	case <-ctx.Done():
		t.Fatal("notification waited for heartbeat")
	}
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	r.cancellationMu.Lock()
	remaining := len(r.runningCancellations)
	r.cancellationMu.Unlock()
	if remaining != 0 {
		t.Fatal("finished worker retained cancellation context")
	}
}

func TestRunnerCancellationCleanupKeepsNewLeaseRegistration(t *testing.T) {
	var r Runner
	id := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	oldCtx, oldCancel := context.WithCancelCause(context.Background())
	defer oldCancel(nil)
	newCtx, newCancel := context.WithCancelCause(context.Background())
	defer newCancel(nil)
	removeOld := r.registerCancellation(domain.JobLease{Job: domain.Job{ID: id}, Generation: 1}, oldCancel)
	removeNew := r.registerCancellation(domain.JobLease{Job: domain.Job{ID: id}, Generation: 2}, newCancel)
	removeOld()
	r.NotifyJobCancellation(id)
	if context.Cause(newCtx) != errCancelRequested || oldCtx.Err() != nil {
		t.Fatal("old lease cleanup removed or redirected notification")
	}
	removeNew()
	if len(r.runningCancellations) != 0 {
		t.Fatal("registry did not release context")
	}
}
