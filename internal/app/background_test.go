package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/testutil"
)

func noopRun(context.Context, context.Context) (bool, error) { return false, nil }

func TestBackgroundTaskValidate(t *testing.T) {
	valid := BackgroundTask{Kind: domain.JobPlaybackProgressFlush, Interval: time.Second, Run: noopRun}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*BackgroundTask){
		"unknown kind":              func(t *BackgroundTask) { t.Kind = domain.JobInventoryScan },
		"no run":                    func(t *BackgroundTask) { t.Run = nil },
		"no interval":               func(t *BackgroundTask) { t.Interval = 0 },
		"negative wake delay":       func(t *BackgroundTask) { t.WakeDelay = -time.Second },
		"negative grace":            func(t *BackgroundTask) { t.StopGrace = -time.Second },
		"poll with wake delay":      func(t *BackgroundTask) { t.IdleWait, t.WakeDelay = true, time.Second },
		"instance kind as cluster":  func(t *BackgroundTask) { t.Cluster = true },
		"cluster kind per instance": func(t *BackgroundTask) { t.Kind = domain.JobWebhookDelivery },
		"unknown class":             func(t *BackgroundTask) { t.Class = WorkIO + 1 },
	} {
		task := valid
		change(&task)
		if err := task.Validate(); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

// A poll runs at once, runs again at once when asked to, waits the
// interval otherwise and wakes early.
func TestRunBackgroundPoll(t *testing.T) {
	testutil.TrackGoroutines(t)
	wake := make(chan struct{}, 1)
	var runs atomic.Int32
	ran := make(chan int32, 16)
	task := BackgroundTask{Kind: domain.JobWebhookDelivery, Interval: time.Hour, IdleWait: true, Wake: wake, Cluster: true,
		Run: func(context.Context, context.Context) (bool, error) {
			n := runs.Add(1)
			ran <- n
			return n < 3, nil
		}}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); RunBackground(ctx, task, nil) }()
	for want := int32(1); want <= 3; want++ {
		select {
		case n := <-ran:
			if n != want {
				t.Fatalf("run %d, want %d", n, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("run %d did not start at once", want)
		}
	}
	select {
	case <-ran:
		t.Fatal("ran again without being asked")
	case <-time.After(50 * time.Millisecond):
	}
	wake <- struct{}{}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("a wake did not end the idle wait")
	}
	cancel()
	<-done
}

// A fixed cadence waits one interval for its first run; a burst of wakes
// is handled by one run after the wake delay.
func TestRunBackgroundCadenceAndWakeDelay(t *testing.T) {
	testutil.TrackGoroutines(t)
	wake := make(chan struct{}, 1)
	ran := make(chan time.Time, 16)
	task := BackgroundTask{Kind: domain.JobWatchStatsAggregation, Interval: time.Hour, Wake: wake, WakeDelay: 30 * time.Millisecond, Cluster: true,
		Run: func(context.Context, context.Context) (bool, error) {
			ran <- time.Now()
			return false, nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); RunBackground(ctx, task, nil) }()
	select {
	case <-ran:
		t.Fatal("ran before the first interval")
	case <-time.After(50 * time.Millisecond):
	}
	woken := time.Now()
	for range 5 {
		select {
		case wake <- struct{}{}:
		default:
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case at := <-ran:
		if at.Sub(woken) < 25*time.Millisecond {
			t.Fatalf("ran %v after the wake, before the delay", at.Sub(woken))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a wake did not run the task")
	}
	select {
	case <-ran:
		t.Fatal("a burst of wakes ran more than once")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	<-done

	// Without a wake delay a wake runs at once; the interval keeps going.
	task.WakeDelay, task.Interval, task.Kind, task.Cluster = 0, 20*time.Millisecond, domain.JobPlaybackProgressFlush, false
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { defer close(done); RunBackground(ctx, task, nil) }()
	for range 3 {
		select {
		case <-ran:
		case <-time.After(5 * time.Second):
			t.Fatal("the interval did not run the task")
		}
	}
	cancel()
	<-done
}

// Stopping cancels the stop context at once and the work context after the
// grace; RunBackground returns when the run does.
func TestRunBackgroundStopGrace(t *testing.T) {
	testutil.TrackGoroutines(t)
	for _, grace := range []time.Duration{0, 80 * time.Millisecond} {
		started := make(chan struct{})
		var stopAt, workAt time.Time
		var mu sync.Mutex
		task := BackgroundTask{Kind: domain.JobWebhookDelivery, Interval: time.Hour, IdleWait: true, Cluster: true, StopGrace: grace,
			Run: func(stop, work context.Context) (bool, error) {
				close(started)
				<-stop.Done()
				mu.Lock()
				stopAt = time.Now()
				mu.Unlock()
				<-work.Done()
				mu.Lock()
				workAt = time.Now()
				mu.Unlock()
				return false, work.Err()
			}}
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
		mu.Lock()
		gap := workAt.Sub(stopAt)
		mu.Unlock()
		if grace == 0 && gap > 50*time.Millisecond || grace > 0 && gap < grace/2 {
			t.Fatalf("grace %v: work cancelled %v after stop", grace, gap)
		}
	}
}

// The tasks keep the settings of the loops they replace.
func TestBackgroundTasksOfTheThreeLoops(t *testing.T) {
	d, _, _, _ := dispatcherFixture(t, domain.DefaultWebhookRetryPolicy())
	webhook := d.Task()
	if webhook.Validate() != nil || webhook.Kind != domain.JobWebhookDelivery || !webhook.IdleWait || webhook.Interval != time.Second ||
		webhook.StopGrace != 5*time.Second || !webhook.Cluster || webhook.Class != 0 || webhook.Wake == nil {
		t.Fatalf("webhook task %+v", webhook)
	}
	stats, err := NewWatchStats(&fakeWatchStatsRepo{}, WatchStatsOptions{Interval: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	aggregation := stats.Task()
	if aggregation.Validate() != nil || aggregation.Kind != domain.JobWatchStatsAggregation || aggregation.IdleWait || aggregation.Interval != 2*time.Minute ||
		aggregation.WakeDelay != 5*time.Second || !aggregation.Cluster || aggregation.Class != WorkIO || aggregation.Wake == nil {
		t.Fatalf("aggregation task %+v", aggregation)
	}
	p, _, _ := newTestProgress(t, ProgressOptions{FlushInterval: 3 * time.Second})
	flush := p.Task()
	if flush.Validate() != nil || flush.Kind != domain.JobPlaybackProgressFlush || flush.IdleWait || flush.Interval != 3*time.Second ||
		flush.WakeDelay != 0 || flush.Cluster || flush.Class != WorkIO || flush.Wake == nil {
		t.Fatalf("flush task %+v", flush)
	}
}

// The webhook task asks for the next round at once only after a full
// batch, and reports a failed round.
func TestWebhookTaskRunsAgainOnFullBatch(t *testing.T) {
	d, store, _, _ := dispatcherFixture(t, domain.DefaultWebhookRetryPolicy())
	d.opts.Batch = 1
	task := d.Task()
	ctx := context.Background()
	again, err := task.Run(ctx, ctx)
	if err != nil || !again || len(store.attempts) != 1 {
		t.Fatalf("full batch again=%t err=%v attempts=%d", again, err, len(store.attempts))
	}
	again, err = task.Run(ctx, ctx)
	if err != nil || again {
		t.Fatalf("empty round again=%t err=%v", again, err)
	}
}

// The flush task maintains storage every sixth successful flush; a failed
// flush is not counted.
func TestProgressTaskMaintainsEverySixFlushes(t *testing.T) {
	p, repo, clock := newTestProgress(t, ProgressOptions{Retention: 24 * time.Hour})
	task := p.Task()
	ctx := context.Background()
	run := func() error {
		clock.Advance(2 * time.Hour)
		_, err := task.Run(ctx, ctx)
		return err
	}
	for range 5 {
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	if len(repo.purged) != 0 {
		t.Fatalf("maintained after %d flushes", 5)
	}
	// A failing flush needs a dirty session.
	if err := p.Report(ctx, progressActor(1), report(domain.PlaybackReportStart, "k", progressItem(1), 0)); err != nil {
		t.Fatal(err)
	}
	if err := p.Report(ctx, progressActor(1), report(domain.PlaybackReportProgress, "k", "", time.Second)); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	repo.failFlush = domain.ErrDatabase
	repo.mu.Unlock()
	if err := run(); !errors.Is(err, domain.ErrDatabase) || len(repo.purged) != 0 {
		t.Fatalf("failed flush err=%v purged=%d", err, len(repo.purged))
	}
	repo.mu.Lock()
	repo.failFlush = nil
	repo.mu.Unlock()
	if err := run(); err != nil || len(repo.purged) != 1 {
		t.Fatalf("sixth successful flush err=%v purged=%d", err, len(repo.purged))
	}
}

// The aggregation task reports an aggregation failure and still applies the
// retention.
func TestWatchStatsTaskReportsFailures(t *testing.T) {
	repo := &fakeWatchStatsRepo{}
	w, err := NewWatchStats(repo, WatchStatsOptions{Retention: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	task := w.Task()
	ctx := context.Background()
	if _, err := task.Run(ctx, ctx); err != nil || repo.batches != 1 || len(repo.purged) != 1 {
		t.Fatalf("run err=%v batches=%d purged=%d", err, repo.batches, len(repo.purged))
	}
	failing := &failingWatchStatsRepo{fakeWatchStatsRepo: repo}
	w, err = NewWatchStats(failing, WatchStatsOptions{Retention: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Task().Run(ctx, ctx); !errors.Is(err, domain.ErrDatabase) || len(repo.purged) != 2 {
		t.Fatalf("failing run err=%v purged=%d", err, len(repo.purged))
	}
}

type failingWatchStatsRepo struct{ *fakeWatchStatsRepo }

func (failingWatchStatsRepo) AggregateWatchStats(context.Context, int, func(domain.WatchStatsBatch) (domain.WatchStatsOutcome, error)) (int, error) {
	return 0, domain.ErrDatabase
}

// recordingBudget admits or refuses I/O permits and records them.
type recordingBudget struct {
	mu       sync.Mutex
	refuse   error
	held     int
	acquired int
	deadline time.Time
}

func (b *recordingBudget) Acquire(ctx context.Context, class WorkClass) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deadline, _ = ctx.Deadline()
	if class != WorkIO {
		return nil, domain.ErrInvalid
	}
	if b.refuse != nil {
		return nil, b.refuse
	}
	b.acquired++
	b.held++
	var once sync.Once
	return func() { once.Do(func() { b.mu.Lock(); b.held--; b.mu.Unlock() }) }, nil
}

// heldWhileRecording records whether the permit was still held when the
// attempt was recorded.
type heldWhileRecording struct {
	*memWebhookStore
	budget *recordingBudget
	held   int
}

func (s *heldWhileRecording) RecordAttempt(ctx context.Context, r WebhookAttemptRecord) error {
	s.budget.mu.Lock()
	s.held = s.budget.held
	s.budget.mu.Unlock()
	return s.memWebhookStore.RecordAttempt(ctx, r)
}

// Every attempt holds one I/O permit for its request only (G41.3); a
// budget without room skips the attempt instead of recording a failure, so
// backpressure never moves a delivery toward the dead letter.
func TestWebhookDispatcherAdmitsAttemptsThroughTheBudget(t *testing.T) {
	_, store, deliverer, clock := dispatcherFixture(t, domain.DefaultWebhookRetryPolicy())
	budget := &recordingBudget{refuse: domain.ErrResourceBusy}
	recording := &heldWhileRecording{memWebhookStore: store, budget: budget}
	d, err := NewWebhookDispatcher(store, recording, prefixBox{}, deliverer, WebhookDispatcherOptions{Clock: clock, Jitter: func() float64 { return 0.5 }, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	round, err := d.RunOnce(ctx)
	if err != nil || round.Claimed != 1 || round.Skipped != 1 || len(deliverer.requests) != 0 || len(store.attempts) != 0 ||
		store.deliveries[0].state != domain.WebhookDeliveryPending || store.deliveries[0].Attempts != 0 {
		t.Fatalf("refused round %+v %v", round, err)
	}
	// The wait for a permit ends early enough for the attempt and its
	// recording to finish inside the lease (2 minutes by default).
	want := 2*time.Minute - DefaultWebhookTimeout - 2*webhookRecordTimeout
	if wait := time.Until(budget.deadline); wait > want || wait < want-5*time.Second {
		t.Fatalf("permit wait %v, want %v", wait, want)
	}
	budget.mu.Lock()
	budget.refuse = nil
	budget.mu.Unlock()
	clock.Add(3 * time.Minute) // the skipped claim's lease expired
	round, err = d.RunOnce(ctx)
	if err != nil || round.Delivered != 1 || budget.acquired != 1 || budget.held != 0 || recording.held != 0 {
		t.Fatalf("admitted round %+v %v acquired=%d held=%d whileRecording=%d", round, err, budget.acquired, budget.held, recording.held)
	}
}
