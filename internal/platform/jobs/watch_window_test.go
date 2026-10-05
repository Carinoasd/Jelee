package jobs

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type windowWatchRepository struct {
	app.WatchRepository
	claims   atomic.Int32
	released chan string
	dirty    chan bool
}

func (r *windowWatchRepository) ClaimWatch(context.Context, string, time.Duration) (domain.WatchLease, error) {
	n := r.claims.Add(1)
	return domain.WatchLease{LibraryID: "library", Generation: int64(n)}, nil
}
func (r *windowWatchRepository) ReleaseWatch(_ context.Context, _ domain.WatchLease, code string) error {
	r.released <- code
	return nil
}
func (r *windowWatchRepository) MarkWatchDirty(context.Context, domain.WatchLease) error {
	r.dirty <- true
	return nil
}
func (r *windowWatchRepository) RenewWatch(context.Context, domain.WatchLease, time.Duration) (bool, error) {
	return true, nil
}

type windowObserver struct{ entered, joined chan bool }

func (o windowObserver) Observe(ctx context.Context, _ []domain.ScanDirectory, changed func(context.Context) error) error {
	if err := changed(ctx); err != nil {
		return err
	}
	o.entered <- true
	<-ctx.Done()
	o.joined <- true
	return ctx.Err()
}

type windowDispatcher struct{}

func (windowDispatcher) DispatchWatch(context.Context, domain.WatchLease) (bool, error) {
	return false, nil
}

func TestWatchWindowStopsObservationAndReopensDirty(t *testing.T) {
	window := &switchWindow{}
	clock := newTestClock()
	repo := &windowWatchRepository{released: make(chan string, 4), dirty: make(chan bool, 4)}
	observer := windowObserver{entered: make(chan bool, 4), joined: make(chan bool, 4)}
	w, err := NewWatchRunnerWithWindow(repo, observer, windowDispatcher{}, slog.New(slog.NewTextHandler(io.Discard, nil)), window)
	if err != nil {
		t.Fatal(err)
	}
	w.clock = clock
	if err = w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := w.Stop(ctx); err != nil {
			t.Error(err)
		}
		if clock.count(0) != 0 {
			t.Error("watch timers leaked")
		}
	}()
	clock.waitFor(t, 0, 1)
	clock.fire(0)
	clock.waitFor(t, time.Second, 1)
	if repo.claims.Load() != 0 {
		t.Fatal("closed window claimed watch")
	}
	window.open.Store(true)
	clock.fire(time.Second)
	receive(t, observer.entered)
	receive(t, repo.dirty)
	clock.waitFor(t, time.Second, 1)
	window.open.Store(false)
	clock.fire(time.Second)
	receive(t, observer.joined)
	clock.waitFor(t, time.Second, 1)
	clock.fire(time.Second)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	released := false
	for !released {
		select {
		case code := <-repo.released:
			if code != "" {
				t.Fatal("window closure recorded observer failure")
			}
			released = true
		case <-clock.changed:
			clock.fire(time.Second)
		case <-deadline.C:
			t.Fatal("observer did not release after joining")
		}
	}
	clock.waitFor(t, time.Second, 1)
	if repo.claims.Load() != 1 {
		t.Fatal("closed window reclaimed observer")
	}
	window.open.Store(true)
	clock.fire(time.Second)
	receive(t, observer.entered)
	receive(t, repo.dirty)
	if repo.claims.Load() != 2 {
		t.Fatal("reopened window did not recreate observer")
	}
}
