package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type cancellationReadRepo struct {
	app.JobExecutionRepository
	read func(context.Context, domain.JobLease) (bool, error)
}

func (r cancellationReadRepo) ReadJobCancellation(ctx context.Context, lease domain.JobLease) (bool, error) {
	return r.read(ctx, lease)
}

func TestRunnerCancellationReadDoesNotAccelerateHeartbeat(t *testing.T) {
	clock := newTestClock()
	var reads, heartbeats atomic.Int64
	repo := cancellationReadRepo{JobExecutionRepository: &executionFake{heartbeat: func(context.Context, domain.JobLease, time.Duration) (bool, error) {
		heartbeats.Add(1)
		return false, nil
	}}, read: func(context.Context, domain.JobLease) (bool, error) { return reads.Add(1) == 2, nil }}
	opts := DefaultOptions()
	opts.Clock = clock
	r, err := New(repo, scannerFunc(nil), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	started, done := make(chan struct{}), make(chan struct{})
	go r.monitor(ctx, domain.JobLease{}, cancel, started, done)
	<-started
	clock.fire(time.Second)
	clock.waitFor(t, time.Second, 1)
	if reads.Load() != 1 || heartbeats.Load() != 0 {
		t.Fatal("flag read changed heartbeat frequency")
	}
	clock.fire(10 * time.Second)
	clock.waitFor(t, 10*time.Second, 1)
	if heartbeats.Load() != 1 {
		t.Fatal("heartbeat was not retained")
	}
	clock.fire(time.Second)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("committed cancellation did not stop monitor")
	}
	if context.Cause(ctx) != errCancelRequested || reads.Load() != 2 || clock.count(time.Second) != 0 || clock.count(10*time.Second) != 0 {
		t.Fatal("monitor leaked timer or missed cancellation")
	}
}

func TestRunnerCancellationReadFailsClosed(t *testing.T) {
	for _, failure := range []error{domain.ErrJobLeaseLost, domain.ErrDatabase} {
		t.Run(failure.Error(), func(t *testing.T) {
			clock := newTestClock()
			repo := cancellationReadRepo{JobExecutionRepository: &executionFake{}, read: func(context.Context, domain.JobLease) (bool, error) { return false, failure }}
			opts := DefaultOptions()
			opts.Clock = clock
			r, err := New(repo, scannerFunc(nil), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			started, done := make(chan struct{}), make(chan struct{})
			go r.monitor(ctx, domain.JobLease{}, cancel, started, done)
			<-started
			clock.fire(time.Second)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("failed read did not stop monitor")
			}
			expected := errHeartbeatFailed
			if errors.Is(failure, domain.ErrJobLeaseLost) {
				expected = domain.ErrJobLeaseLost
			}
			if !errors.Is(context.Cause(ctx), expected) {
				t.Fatal("failed read cause", context.Cause(ctx))
			}
		})
	}
}
