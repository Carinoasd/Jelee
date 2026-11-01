package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// secondInstance opens another pool on the fixture's schema, as a second
// Jelee instance sharing the database would.
func secondInstance(t *testing.T, s *Store) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	other, err := Open(ctx, s.Pool.Config().ConnString(), 8)
	if err != nil {
		t.Fatal("open a second instance")
	}
	t.Cleanup(other.Pool.Close)
	return other
}

func backgroundLane(t *testing.T, s *Store, budget app.WorkBudget) *jobworker.Background {
	t.Helper()
	lane, err := jobworker.NewBackground(jobworker.BackgroundOptions{Locker: s, Budget: budget, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return lane
}

func laneOutcomes(lane *jobworker.Background, kind string) [4]uint64 {
	return lane.BackgroundJobMetrics().Groups[domain.BackgroundJobKindIndex(kind)].Outcomes
}

// The cluster-wide lock of a background kind is held by one session at a
// time across instances, per kind; release returns the connection and a
// cancelled run gives the lock back.
func TestBackgroundLockPostgres(t *testing.T) {
	f := newJobFixture(t)
	other := secondInstance(t, f.s)
	// A failing check must not leave a connection out of its pool, or
	// closing the pool waits for it.
	held := func(release func(), acquired bool, err error) (func(), bool, error) {
		if acquired {
			t.Cleanup(release)
		}
		return release, acquired, err
	}
	release, acquired, err := held(f.s.TryBackgroundLock(f.ctx, domain.JobWebhookDelivery))
	if err != nil || !acquired {
		t.Fatalf("first lock acquired=%t %v", acquired, err)
	}
	for name, s := range map[string]*Store{"other instance": other, "same instance, other session": f.s} {
		if _, acquired, err := held(s.TryBackgroundLock(f.ctx, domain.JobWebhookDelivery)); err != nil || acquired {
			t.Fatalf("%s took a held lock: %t %v", name, acquired, err)
		}
	}
	stats, acquired, err := other.TryBackgroundLock(f.ctx, domain.JobWatchStatsAggregation)
	if err != nil || !acquired {
		t.Fatalf("another kind's lock acquired=%t %v", acquired, err)
	}
	stats()
	for _, kind := range []string{domain.JobPlaybackProgressFlush, domain.JobInventoryScan, ""} {
		if _, _, err := f.s.TryBackgroundLock(f.ctx, kind); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("lock for %q: %v", kind, err)
		}
	}
	if f.s.Pool.Stat().AcquiredConns() != 1 {
		t.Fatalf("held lock keeps %d connections", f.s.Pool.Stat().AcquiredConns())
	}
	release()
	release()
	if f.s.Pool.Stat().AcquiredConns() != 0 || other.Pool.Stat().AcquiredConns() != 0 {
		t.Fatal("released lock kept its connection")
	}
	again, acquired, err := other.TryBackgroundLock(f.ctx, domain.JobWebhookDelivery)
	if err != nil || !acquired {
		t.Fatalf("released lock not free: %t %v", acquired, err)
	}
	again()
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, _, err := f.s.TryBackgroundLock(cancelled, domain.JobWebhookDelivery); err == nil {
		t.Fatal("cancelled lock attempt succeeded")
	}

	// A run stopped while it holds the lock is cancelled and gives it back.
	lane := backgroundLane(t, f.s, nil)
	running := make(chan struct{})
	if err := lane.Register(app.BackgroundTask{Kind: domain.JobWatchStatsAggregation, Interval: time.Millisecond, Cluster: true,
		Run: func(stop, _ context.Context) (bool, error) {
			close(running)
			<-stop.Done()
			return false, stop.Err()
		}}); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(f.ctx)
	t.Cleanup(func() { stop(); lane.Wait() })
	if err := lane.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-running
	if _, acquired, _ := held(other.TryBackgroundLock(f.ctx, domain.JobWatchStatsAggregation)); acquired {
		t.Fatal("lock free while the run holds it")
	}
	stop()
	lane.Wait()
	if got := laneOutcomes(lane, domain.JobWatchStatsAggregation); got[2] != 1 {
		t.Fatalf("stopped run outcomes %v", got)
	}
	free, acquired, err := other.TryBackgroundLock(f.ctx, domain.JobWatchStatsAggregation)
	if err != nil || !acquired {
		t.Fatalf("cancelled run kept the lock: %t %v", acquired, err)
	}
	free()
}

// webhookInstanceDeliverer delivers instantly after a short pause and
// notices deliveries in flight on the other instance at the same time.
type webhookInstanceDeliverer struct {
	self, other *atomic.Int32
	overlap     *atomic.Bool
	mu          *sync.Mutex
	sent        map[string]int
}

func (d webhookInstanceDeliverer) Deliver(ctx context.Context, r app.WebhookRequest) (domain.WebhookOutcome, error) {
	d.self.Add(1)
	defer d.self.Add(-1)
	if d.other.Load() != 0 {
		d.overlap.Store(true)
	}
	select {
	case <-time.After(2 * time.Millisecond):
	case <-ctx.Done():
		return domain.WebhookOutcome{}, ctx.Err()
	}
	if d.other.Load() != 0 {
		d.overlap.Store(true)
	}
	d.mu.Lock()
	d.sent[r.Headers[domain.WebhookEventIDHeader]]++
	d.mu.Unlock()
	return domain.WebhookOutcome{Kind: domain.WebhookOutcomeDelivered, StatusCode: 204}, nil
}

// Two instances run the webhook_delivery job on their background lanes
// against one database: only one delivers at a time, every delivery is
// sent once and recorded delivered, and the shared budget is back at zero.
func TestBackgroundWebhookDeliveryOneInstancePostgres(t *testing.T) {
	f := newJobFixture(t)
	f.s.EnableWebhookEvents(true)
	createWebhook(t, f, nil, domain.WebhookScanCompleted)
	const events = 24
	for i := range events {
		event, err := domain.NewWebhookEvent(fmt.Sprintf("evt-lane-%02d", i), domain.WebhookScanCompleted, time.Now(),
			domain.WebhookSubject{Kind: domain.WebhookSubjectLibrary, ID: f.registration.Library.ID}, map[string]any{"missing": 0})
		if err != nil {
			t.Fatal(err)
		}
		if err = f.s.Append(f.ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 2, Total: 2, Queue: 8})
	if err != nil {
		t.Fatal(err)
	}
	var inFlight [2]atomic.Int32
	var overlap atomic.Bool
	var mu sync.Mutex
	sent := map[string]int{}
	stores := []*Store{f.s, secondInstance(t, f.s)}
	lanes := make([]*jobworker.Background, 2)
	for i, s := range stores {
		deliverer := webhookInstanceDeliverer{self: &inFlight[i], other: &inFlight[1-i], overlap: &overlap, mu: &mu, sent: sent}
		dispatcher, err := app.NewWebhookDispatcher(s, s, webhookBox(t, 7), deliverer, app.WebhookDispatcherOptions{
			Batch: 5, Concurrency: 2, PollInterval: 10 * time.Millisecond, Budget: budget})
		if err != nil {
			t.Fatal(err)
		}
		lanes[i] = backgroundLane(t, s, budget)
		if err = lanes[i].Register(dispatcher.Task()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, stop := context.WithCancel(f.ctx)
	t.Cleanup(func() {
		stop()
		for _, lane := range lanes {
			lane.Wait()
		}
	})
	for _, lane := range lanes {
		if err := lane.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for syncCount(t, f, `SELECT count(*) FROM webhook_deliveries WHERE state='delivered'`) != events {
		if time.Now().After(deadline) {
			t.Fatal("deliveries did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	for _, lane := range lanes {
		lane.Wait()
	}
	if overlap.Load() {
		t.Fatal("both instances delivered at the same time")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != events {
		t.Fatalf("%d distinct deliveries sent, want %d", len(sent), events)
	}
	for key, n := range sent {
		if n != 1 {
			t.Fatalf("%s sent %d times", key, n)
		}
	}
	if n := syncCount(t, f, `SELECT count(*) FROM webhook_delivery_attempts`); n != events {
		t.Fatalf("attempts %d", n)
	}
	if budget.Stats() != (resources.Stats{}) {
		t.Fatalf("budget left %+v", budget.Stats())
	}
	var succeeded uint64
	for _, lane := range lanes {
		succeeded += laneOutcomes(lane, domain.JobWebhookDelivery)[0]
	}
	if succeeded == 0 {
		t.Fatal("no successful delivery run counted")
	}
}

// Two instances run the watch statistics roll-up and their own progress
// flush on their lanes: buffered reports reach the database through the
// flush job, and every ended session is counted once.
func TestBackgroundStatsAndProgressLanesPostgres(t *testing.T) {
	f := newWatchStatsFixture(t)
	f.play(t, f.viewer, "lane-one", f.item, 0, 5*time.Minute, false)
	f.play(t, f.viewer, "lane-two", f.item, 0, 3*time.Minute, false)
	// A third session keeps playing on a buffer flushed by the lane: its
	// progress waits in memory until the flush job writes it.
	flusher, err := app.NewProgress(f.s, f.s, app.ProgressOptions{Clock: f.clock, FlushInterval: 10 * time.Millisecond, SessionTimeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	open := func(kind domain.PlaybackReportKind, item string, position time.Duration) {
		t.Helper()
		if err := flusher.Report(f.ctx, f.viewer, domain.PlaybackReport{Kind: kind, PlayKey: "lane-open", ItemID: item, PositionTicks: domain.DurationToTicks(position), PositionKnown: true}); err != nil {
			t.Fatal(err)
		}
	}
	open(domain.PlaybackReportStart, f.item, 0)
	f.clock.Advance(30 * time.Second)
	open(domain.PlaybackReportProgress, "", 2*time.Minute)
	if n := f.count(t, `SELECT count(*) FROM playback_sessions WHERE play_key='lane-open' AND position_ticks>0`); n != 0 {
		t.Fatal("progress written before a flush")
	}
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 8})
	if err != nil {
		t.Fatal(err)
	}
	stores := []*Store{f.s, secondInstance(t, f.s)}
	lanes := make([]*jobworker.Background, 2)
	for i, s := range stores {
		stats, err := app.NewWatchStats(s, app.WatchStatsOptions{Clock: f.clock, Interval: 5 * time.Millisecond, WakeDelay: time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		lanes[i] = backgroundLane(t, s, budget)
		if err = lanes[i].Register(stats.Task()); err != nil {
			t.Fatal(err)
		}
	}
	if err = lanes[0].Register(flusher.Task()); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(f.ctx)
	t.Cleanup(func() {
		stop()
		for _, lane := range lanes {
			lane.Wait()
		}
	})
	for _, lane := range lanes {
		if err := lane.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		pending, err := f.s.PendingWatchStats(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		position := f.count(t, `SELECT COALESCE(max(position_ticks),0)::int FROM playback_sessions WHERE play_key='lane-open' AND state='active'`)
		if pending == 0 && f.daily(t, f.viewer.UserID).sessions == 2 && int64(position) == domain.DurationToTicks(2*time.Minute) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending=%d daily=%+v position=%d", pending, f.daily(t, f.viewer.UserID), position)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	for _, lane := range lanes {
		lane.Wait()
	}
	// Aggregating once more finds nothing: no session was counted twice.
	if n := f.aggregate(t); n != 0 {
		t.Fatalf("aggregated %d again", n)
	}
	if r := f.daily(t, f.viewer.UserID); r.sessions != 2 || r.effective <= 0 {
		t.Fatalf("daily %+v", r)
	}
	if budget.Stats() != (resources.Stats{}) {
		t.Fatalf("budget left %+v", budget.Stats())
	}
	if got := laneOutcomes(lanes[0], domain.JobPlaybackProgressFlush); got[0] == 0 {
		t.Fatalf("flush outcomes %v", got)
	}
}

// Two instances run the subtitle_ocr job against one database: the
// session advisory lock lets one recognize at a time, the other's runs are
// skipped with its queue left for later, and it takes over once the lock is
// free. Each run is one queued source, as subtitleocr.Service.Task does.
func TestBackgroundSubtitleOCROneInstancePostgres(t *testing.T) {
	f := newJobFixture(t)
	stores := []*Store{f.s, secondInstance(t, f.s)}
	const sources = 6
	var inFlight atomic.Int32
	var overlap atomic.Bool
	var queued [2]atomic.Int32
	gate, holding := make(chan struct{}), make(chan struct{})
	var holdOnce sync.Once
	lanes := make([]*jobworker.Background, 2)
	for i, s := range stores {
		queued[i].Store(sources)
		lanes[i] = backgroundLane(t, s, nil)
		err := lanes[i].Register(app.BackgroundTask{Kind: domain.JobSubtitleOCR, Interval: 5 * time.Millisecond, IdleWait: true, Cluster: true,
			Run: func(_, work context.Context) (bool, error) {
				if queued[i].Load() == 0 {
					return false, nil
				}
				if inFlight.Add(1) != 1 {
					overlap.Store(true)
				}
				defer inFlight.Add(-1)
				if i == 0 {
					// The first instance's first source keeps the lock
					// until the test has seen the other one skip.
					holdOnce.Do(func() {
						close(holding)
						select {
						case <-gate:
						case <-work.Done():
						}
					})
				}
				select {
				case <-time.After(2 * time.Millisecond):
				case <-work.Done():
					return false, work.Err()
				}
				return queued[i].Add(-1) > 0, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, stop := context.WithCancel(f.ctx)
	t.Cleanup(func() {
		stop()
		for _, lane := range lanes {
			lane.Wait()
		}
	})
	if err := lanes[0].Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-holding
	if err := lanes[1].Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for laneOutcomes(lanes[1], domain.JobSubtitleOCR)[3] < 3 {
		if time.Now().After(deadline) {
			t.Fatal("second instance never skipped")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if queued[1].Load() != sources || lanes[1].BackgroundJobMetrics().Groups[domain.BackgroundJobKindIndex(domain.JobSubtitleOCR)].Running != 0 {
		t.Fatal("second instance recognized while the first held the lock")
	}
	if n := syncCount(t, f, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND granted AND classid=hashtext(current_schema())::oid AND objid=17481270`); n != 1 {
		t.Fatalf("subtitle_ocr lock held by %d sessions", n)
	}
	close(gate)
	for queued[0].Load()+queued[1].Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("queues left %d %d", queued[0].Load(), queued[1].Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	for _, lane := range lanes {
		lane.Wait()
	}
	if overlap.Load() {
		t.Fatal("both instances recognized at the same time")
	}
	for i, lane := range lanes {
		if got := laneOutcomes(lane, domain.JobSubtitleOCR); got[0] < sources || got[1] != 0 {
			t.Fatalf("instance %d outcomes %v", i, got)
		}
	}
	// A lock attempt cancelled by the stop closes its session, whose
	// backend may take a moment to exit and drop the lock.
	for syncCount(t, f, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid=hashtext(current_schema())::oid AND objid=17481270`) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("subtitle_ocr lock left after the lanes stopped")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The pool returns a cancelled lock attempt's connection only after
	// its session has closed, so give that the same deadline.
	for i, s := range stores {
		for s.Pool.Stat().AcquiredConns() != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("instance %d kept %d connections", i, s.Pool.Stat().AcquiredConns())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}
