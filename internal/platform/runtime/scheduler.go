package runtime

import (
	"context"
	"errors"
	"github.com/MoYuanCN/Jelee/internal/app"
	"log/slog"
	"sync"
	"time"
)

type scheduleDispatcher interface {
	DispatchSchedule(context.Context) (bool, error)
}

type scheduledWorker struct {
	worker   serviceWorker
	dispatch scheduleDispatcher
	logger   *slog.Logger
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

func (w *scheduledWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done != nil {
		return errors.New("scheduler already started")
	}
	if err := w.worker.Start(ctx); err != nil {
		return err
	}
	life, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-life.Done():
				return
			case <-timer.C:
			}
			// At most one short dispatch per second; errors and an empty queue
			// back off for five seconds. No in-memory catch-up queue is created.
			call, stop := context.WithTimeout(life, 3*time.Second)
			worked, err := w.dispatch.DispatchSchedule(call)
			stop()
			delay := 5 * time.Second
			if worked && err == nil {
				delay = time.Second
			}
			if err != nil && life.Err() == nil {
				w.logger.Warn("schedule dispatch failed", "component", "jobs", "code", "schedule_dispatch_failed")
			}
			timer.Reset(delay)
		}
	}()
	return nil
}

func (w *scheduledWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	workerErr := w.worker.Stop(ctx)
	if done == nil {
		return workerErr
	}
	select {
	case <-done:
		return workerErr
	case <-ctx.Done():
		return errors.Join(workerErr, ctx.Err())
	}
}

func (w *scheduledWorker) NotifyJobCancellation(id string) {
	if notifier, ok := w.worker.(app.JobCancellationNotifier); ok {
		notifier.NotifyJobCancellation(id)
	}
}
