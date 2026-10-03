package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type WatchDispatcher interface {
	DispatchWatch(context.Context, domain.WatchLease) (bool, error)
}

type WatchRunner struct {
	window     WorkWindow
	clock      Clock
	now        func() time.Time
	repository app.WatchRepository
	observer   app.DirectoryObserver
	dispatcher WatchDispatcher
	logger     *slog.Logger
	owner      string
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
}

func NewWatchRunner(repository app.WatchRepository, observer app.DirectoryObserver, dispatcher WatchDispatcher, logger *slog.Logger) (*WatchRunner, error) {
	return NewWatchRunnerWithWindow(repository, observer, dispatcher, logger, nil)
}

// NewWatchRunnerWithWindow keeps directory traversal inside the job window.
// Reopened observers emit their normal initial dirty signal for missed changes.
func NewWatchRunnerWithWindow(repository app.WatchRepository, observer app.DirectoryObserver, dispatcher WatchDispatcher, logger *slog.Logger, window WorkWindow) (*WatchRunner, error) {
	if repository == nil || observer == nil || dispatcher == nil || logger == nil {
		return nil, domain.ErrInvalid
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, errors.New("cannot create watch owner")
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	raw := hex.EncodeToString(id[:])
	owner := raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
	return &WatchRunner{window: window, clock: realClock{}, now: time.Now, repository: repository, observer: observer, dispatcher: dispatcher, logger: logger, owner: owner}, nil
}

func (w *WatchRunner) Start(ctx context.Context) error {
	if ctx == nil {
		return domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done != nil {
		return errors.New("watch runner already started")
	}
	life, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})
	go func() { defer close(w.done); w.run(life) }()
	return nil
}

func (w *WatchRunner) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type activeWatch struct {
	lease      domain.WatchLease
	cancel     context.CancelFunc
	done       chan error
	renewAt    time.Time
	dispatchAt time.Time
	stopping   bool
}

func (w *WatchRunner) run(ctx context.Context) {
	active := make(map[string]*activeWatch)
	defer func() {
		for _, watch := range active {
			watch.cancel()
		}
		for _, watch := range active {
			<-watch.done
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for _, watch := range active {
			_ = w.repository.ReleaseWatch(cleanup, watch.lease, "")
		}
	}()
	timer := w.clock.NewTimer(0)
	defer func() { timer.Stop() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C():
		}
		now := w.now()
		allowed := w.window == nil || w.window.Allows(now)
		for library, watch := range active {
			select {
			case err := <-watch.done:
				watch.cancel()
				code := ""
				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, domain.ErrJobLeaseLost) {
					code = "observer_unavailable"
					if errors.Is(err, domain.ErrScanLimit) {
						code = "resource_limit"
					}
				}
				call, stop := context.WithTimeout(ctx, 2*time.Second)
				releaseErr := w.repository.ReleaseWatch(call, watch.lease, code)
				stop()
				if (code != "" || releaseErr != nil) && ctx.Err() == nil {
					w.logger.Warn("directory observation stopped", "component", "jobs", "code", "watch_unavailable", "libraryId", library)
				}
				delete(active, library)
				continue
			default:
			}
			if !allowed {
				watch.cancel()
				watch.stopping = true
			}
			if watch.stopping {
				continue
			}
			if !now.Before(watch.renewAt) {
				call, stop := context.WithTimeout(ctx, 2*time.Second)
				live, err := w.repository.RenewWatch(call, watch.lease, 30*time.Second)
				stop()
				if err != nil || !live {
					watch.cancel()
					watch.stopping = true
					continue
				}
				watch.renewAt = now.Add(10 * time.Second)
			}
			if !now.Before(watch.dispatchAt) {
				call, stop := context.WithTimeout(ctx, 2*time.Second)
				_, err := w.dispatcher.DispatchWatch(call, watch.lease)
				stop()
				if errors.Is(err, domain.ErrJobLeaseLost) {
					watch.cancel()
					watch.stopping = true
				}
				if err != nil && !errors.Is(err, domain.ErrJobLeaseLost) && ctx.Err() == nil {
					w.logger.Warn("watch scan dispatch failed", "component", "jobs", "code", "watch_dispatch_failed", "libraryId", library)
				}
				watch.dispatchAt = now.Add(5 * time.Second)
			}
		}
		if allowed && len(active) < domain.MaxWatchLibraries && ctx.Err() == nil {
			call, stop := context.WithTimeout(ctx, 2*time.Second)
			lease, err := w.repository.ClaimWatch(call, w.owner, 30*time.Second)
			stop()
			if err == nil {
				if w.window != nil && !w.window.Allows(w.now()) {
					call, stop := context.WithTimeout(ctx, 2*time.Second)
					_ = w.repository.ReleaseWatch(call, lease, "")
					stop()
				} else if previous := active[lease.LibraryID]; previous != nil {
					previous.cancel()
					previous.stopping = true
					call, stop := context.WithTimeout(ctx, 2*time.Second)
					_ = w.repository.ReleaseWatch(call, lease, "")
					stop()
				} else {
					life, cancel := context.WithCancel(ctx)
					watch := &activeWatch{lease: lease, cancel: cancel, done: make(chan error, 1), renewAt: now.Add(10 * time.Second)}
					active[lease.LibraryID] = watch
					go func() {
						watch.done <- w.observer.Observe(life, lease.Roots, func(eventContext context.Context) error {
							call, stop := context.WithTimeout(eventContext, 2*time.Second)
							defer stop()
							return w.repository.MarkWatchDirty(call, lease)
						})
					}()
				}
			} else if !errors.Is(err, domain.ErrNotFound) && ctx.Err() == nil {
				w.logger.Warn("watch claim failed", "component", "jobs", "code", "watch_claim_failed")
			}
		}
		timer.Stop()
		timer = w.clock.NewTimer(time.Second)
	}
}
