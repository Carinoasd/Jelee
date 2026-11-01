package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/MoYuanCN/Jelee/internal/platform/testutil"
)

// fakeLocker grants a lock unless another holder has it, or fails.
type fakeLocker struct {
	mu       sync.Mutex
	held     map[string]bool
	err      error
	acquired int
	released int
}

func (l *fakeLocker) TryBackgroundLock(_ context.Context, kind string) (func(), bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, false, l.err
	}
	if l.held[kind] {
		return nil, false, nil
	}
	if l.held == nil {
		l.held = map[string]bool{}
	}
	l.held[kind] = true
	l.acquired++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.held[kind] = false
			l.released++
		})
	}, true, nil
}

func (l *fakeLocker) isHeld(kind string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held[kind]
}

func backgroundLogger(buffer *bytes.Buffer) *slog.Logger {
	if buffer == nil {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(slog.NewTextHandler(buffer, nil))
}

func group(t *testing.T, b *Background, kind string) app.BackgroundJobGroup {
	t.Helper()
	return b.BackgroundJobMetrics().Groups[domain.BackgroundJobKindIndex(kind)]
}

func outcomes(g app.BackgroundJobGroup) map[string]uint64 {
	out := map[string]uint64{}
	for i, name := range domain.BackgroundRunOutcomes() {
		if g.Outcomes[i] > 0 {
			out[name] = g.Outcomes[i]
		}
	}
	return out
}

func TestBackgroundRegister(t *testing.T) {
	if _, err := NewBackground(BackgroundOptions{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing logger accepted: %v", err)
	}
	b, err := NewBackground(BackgroundOptions{Logger: backgroundLogger(nil)})
	if err != nil {
		t.Fatal(err)
	}
	run := func(context.Context, context.Context) (bool, error) { return false, nil }
	cluster := app.BackgroundTask{Kind: domain.JobWatchStatsAggregation, Interval: time.Minute, Cluster: true, Run: run}
	if err := b.Register(cluster); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("cluster-wide task without a locker accepted: %v", err)
	}
	if err := b.Register(app.BackgroundTask{Kind: "image_variants", Interval: time.Minute, Run: run}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("kind outside the whitelist accepted: %v", err)
	}
	flush := app.BackgroundTask{Kind: domain.JobPlaybackProgressFlush, Interval: time.Minute, Run: run}
	if err := b.Register(flush); err != nil {
		t.Fatal(err)
	}
	if err := b.Register(flush); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("kind registered twice: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Register(app.BackgroundTask{Kind: domain.JobWebhookDelivery, Interval: time.Second, IdleWait: true, Cluster: true, Run: run}); err == nil {
		t.Fatal("registered after start")
	}
	if err := b.Start(ctx); err == nil {
		t.Fatal("started twice")
	}
	cancel()
	b.Wait()
	if kinds := b.Kinds(); len(kinds) != 1 || kinds[0] != domain.JobPlaybackProgressFlush {
		t.Fatalf("kinds %v", kinds)
	}
}

// A cluster-wide run takes the lock for exactly its own duration; with the
// lock held elsewhere it is skipped and never starts, and a lock failure is
// a failed run that is logged.
func TestBackgroundClusterLock(t *testing.T) {
	testutil.TrackGoroutines(t)
	locker := &fakeLocker{}
	var logs bytes.Buffer
	b, err := NewBackground(BackgroundOptions{Locker: locker, Logger: backgroundLogger(&logs)})
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	task := app.BackgroundTask{Kind: domain.JobWatchStatsAggregation, Interval: time.Minute, Cluster: true,
		Run: func(context.Context, context.Context) (bool, error) {
			if !locker.isHeld(domain.JobWatchStatsAggregation) {
				t.Error("ran without the lock")
			}
			runs.Add(1)
			return false, nil
		}}
	run := b.runner(task, domain.BackgroundJobKindIndex(task.Kind))
	ctx := context.Background()
	run(ctx, ctx, time.Now())
	if runs.Load() != 1 || locker.isHeld(task.Kind) || locker.released != 1 {
		t.Fatalf("runs=%d held=%t released=%d", runs.Load(), locker.isHeld(task.Kind), locker.released)
	}
	// Another instance holds the lock.
	locker.held[task.Kind] = true
	run(ctx, ctx, time.Now())
	locker.held[task.Kind] = false
	locker.err = domain.ErrDatabase
	run(ctx, ctx, time.Now())
	g := group(t, b, task.Kind)
	if runs.Load() != 1 || g.Wait.Count != 1 || g.Duration.Count != 1 || g.Running != 0 {
		t.Fatalf("skipped or failed run started: runs=%d %+v", runs.Load(), g)
	}
	if got := outcomes(g); len(got) != 3 || got[domain.BackgroundRunSucceeded] != 1 || got[domain.BackgroundRunSkipped] != 1 || got[domain.BackgroundRunFailed] != 1 {
		t.Fatalf("outcomes %v", got)
	}
	if !strings.Contains(logs.String(), "background_lock_unavailable") || !strings.Contains(logs.String(), "kind="+task.Kind) {
		t.Fatalf("lock failure not logged: %s", logs.String())
	}
}

// A run with a work class holds one permit of the shared budget from start
// to end and nothing else; a saturated budget skips the run after waiting
// at most one interval, a full queue skips it at once.
func TestBackgroundSharedBudget(t *testing.T) {
	testutil.TrackGoroutines(t)
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBackground(BackgroundOptions{Budget: budget, Logger: backgroundLogger(nil)})
	if err != nil {
		t.Fatal(err)
	}
	var during resources.Stats
	task := app.BackgroundTask{Kind: domain.JobPlaybackProgressFlush, Interval: 50 * time.Millisecond, Class: app.WorkIO,
		Run: func(context.Context, context.Context) (bool, error) {
			during = budget.Stats()
			return false, nil
		}}
	run := b.runner(task, domain.BackgroundJobKindIndex(task.Kind))
	ctx := context.Background()
	run(ctx, ctx, time.Now())
	if during != (resources.Stats{IO: 1, Total: 1}) || budget.Stats() != (resources.Stats{}) {
		t.Fatalf("during %+v after %+v", during, budget.Stats())
	}
	// Saturated: the run waits one interval, then is skipped.
	hold, err := budget.Acquire(ctx, app.WorkCPU)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	run(ctx, ctx, time.Now())
	if waited := time.Since(began); waited < 40*time.Millisecond || waited > 2*time.Second {
		t.Fatalf("waited %v for a saturated budget", waited)
	}
	// A full queue refuses at once.
	queued := make(chan struct{})
	waiterCtx, stopWaiter := context.WithCancel(ctx)
	go func() {
		defer close(queued)
		if release, err := budget.Acquire(waiterCtx, app.WorkCPU); err == nil {
			release()
		}
	}()
	for budget.Stats().Waiting != 1 {
		time.Sleep(time.Millisecond)
	}
	began = time.Now()
	run(ctx, ctx, time.Now())
	if waited := time.Since(began); waited > 30*time.Millisecond {
		t.Fatalf("full queue waited %v", waited)
	}
	stopWaiter()
	<-queued
	hold()
	g := group(t, b, task.Kind)
	if got := outcomes(g); got[domain.BackgroundRunSucceeded] != 1 || got[domain.BackgroundRunSkipped] != 2 || g.Duration.Count != 1 {
		t.Fatalf("outcomes %v %+v", got, g)
	}
	// A cluster-wide run refused by the budget gives its lock back.
	locker := &fakeLocker{}
	b, err = NewBackground(BackgroundOptions{Budget: budget, Locker: locker, Logger: backgroundLogger(nil)})
	if err != nil {
		t.Fatal(err)
	}
	stats := app.BackgroundTask{Kind: domain.JobWatchStatsAggregation, Interval: 20 * time.Millisecond, Cluster: true, Class: app.WorkIO,
		Run: func(context.Context, context.Context) (bool, error) {
			t.Error("ran without a permit")
			return false, nil
		}}
	hold, err = budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		t.Fatal(err)
	}
	b.runner(stats, domain.BackgroundJobKindIndex(stats.Kind))(ctx, ctx, time.Now())
	hold()
	if locker.acquired != 1 || locker.released != 1 {
		t.Fatalf("lock acquired=%d released=%d", locker.acquired, locker.released)
	}
}

// Outcomes, waits and durations: a failed run, a run cancelled by the
// stop, and the wait from due to start.
func TestBackgroundOutcomesAndMetrics(t *testing.T) {
	testutil.TrackGoroutines(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	b, err := NewBackground(BackgroundOptions{Logger: backgroundLogger(nil), Now: func() time.Time { return time.Unix(0, clock.Load()).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	var result error
	task := app.BackgroundTask{Kind: domain.JobPlaybackProgressFlush, Interval: time.Second,
		Run: func(context.Context, context.Context) (bool, error) {
			if g := group(t, b, domain.JobPlaybackProgressFlush); g.Running != 1 {
				t.Errorf("running %d during a run", g.Running)
			}
			clock.Add(int64(3 * time.Second))
			return true, result
		}}
	run := b.runner(task, domain.BackgroundJobKindIndex(task.Kind))
	ctx := context.Background()
	// Due 2 s before it starts, takes 3 s.
	if again := run(ctx, ctx, now.Add(-2*time.Second)); !again {
		t.Fatal("a successful run lost its again")
	}
	result = domain.ErrDatabase
	if again := run(ctx, ctx, time.Unix(0, clock.Load())); again {
		t.Fatal("a failed run ran again at once")
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	result = context.Canceled
	run(stopped, ctx, time.Unix(0, clock.Load()))
	// A run cancelled after admission is a cancelled run.
	stopping, cancelLater := context.WithCancel(ctx)
	task.Run = func(context.Context, context.Context) (bool, error) {
		cancelLater()
		return false, context.Canceled
	}
	b.runner(task, domain.BackgroundJobKindIndex(task.Kind))(stopping, ctx, time.Unix(0, clock.Load()))
	g := group(t, b, task.Kind)
	got := outcomes(g)
	if got[domain.BackgroundRunSucceeded] != 1 || got[domain.BackgroundRunFailed] != 1 || got[domain.BackgroundRunCancelled] != 2 || g.Running != 0 {
		t.Fatalf("outcomes %v running %d", got, g.Running)
	}
	// The run cancelled before admission has no samples.
	if g.Wait.Count != 3 || g.Duration.Count != 3 || g.Wait.SumSeconds != 2 || g.Duration.SumSeconds != 6 {
		t.Fatalf("wait %+v duration %+v", g.Wait, g.Duration)
	}
	// 2 s falls into the 2.5 s bucket; 3 s into the 5 s bucket; 0 s into
	// the first.
	if g.Wait.BucketCounts[3] != 1 || g.Wait.BucketCounts[0] != 2 || g.Duration.BucketCounts[4] != 2 || g.Duration.BucketCounts[0] != 1 {
		t.Fatalf("buckets wait %v duration %v", g.Wait.BucketCounts, g.Duration.BucketCounts)
	}
	snapshot := b.BackgroundJobMetrics()
	for i, kind := range domain.BackgroundJobKinds() {
		if snapshot.Groups[i].Kind != kind {
			t.Fatalf("group %d is %q", i, snapshot.Groups[i].Kind)
		}
	}
	if !snapshot.StartedAt.Equal(now) {
		t.Fatalf("started at %v", snapshot.StartedAt)
	}
}

// Started lanes stop with their context: Wait for one kind returns once
// that kind has returned, and the delivery job's in-flight run keeps its
// work context for the stop grace.
func TestBackgroundStartWaitAndStopGrace(t *testing.T) {
	testutil.TrackGoroutines(t)
	locker := &fakeLocker{}
	b, err := NewBackground(BackgroundOptions{Locker: locker, Logger: backgroundLogger(nil)})
	if err != nil {
		t.Fatal(err)
	}
	flushed := make(chan struct{}, 64)
	if err := b.Register(app.BackgroundTask{Kind: domain.JobPlaybackProgressFlush, Interval: 5 * time.Millisecond,
		Run: func(context.Context, context.Context) (bool, error) {
			select {
			case flushed <- struct{}{}:
			default:
			}
			return false, nil
		}}); err != nil {
		t.Fatal(err)
	}
	inFlight := make(chan struct{})
	var finished atomic.Bool
	if err := b.Register(app.BackgroundTask{Kind: domain.JobWebhookDelivery, Interval: time.Hour, IdleWait: true, Cluster: true, StopGrace: 100 * time.Millisecond,
		Run: func(stop, work context.Context) (bool, error) {
			close(inFlight)
			<-stop.Done()
			select {
			case <-work.Done():
				return false, work.Err()
			case <-time.After(20 * time.Millisecond):
				// The attempt finished inside the grace.
				finished.Store(true)
				return false, nil
			}
		}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-flushed
	<-inFlight
	cancel()
	b.Wait(domain.JobPlaybackProgressFlush)
	b.Wait()
	if !finished.Load() || locker.isHeld(domain.JobWebhookDelivery) || locker.released != 1 {
		t.Fatalf("finished=%t held=%t released=%d", finished.Load(), locker.isHeld(domain.JobWebhookDelivery), locker.released)
	}
	if got := outcomes(group(t, b, domain.JobWebhookDelivery)); got[domain.BackgroundRunSucceeded] != 1 {
		t.Fatalf("delivery outcomes %v", got)
	}
	// Waiting for a kind that never started returns at once.
	b.Wait(domain.JobWatchStatsAggregation)
}
