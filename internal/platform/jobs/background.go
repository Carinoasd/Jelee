package jobs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// BackgroundLocker holds the cluster-wide lock of one background kind for
// one run. acquired is false while another instance holds it. release must
// be called exactly once after an acquired run; it never fails, a lock it
// cannot release normally goes with its connection.
type BackgroundLocker interface {
	TryBackgroundLock(ctx context.Context, kind string) (release func(), acquired bool, err error)
}

// BackgroundOptions configure the background job lane.
type BackgroundOptions struct {
	// Budget admits runs of tasks with a work class; nil admits all.
	Budget app.WorkBudget
	// Locker keeps cluster-wide tasks to one instance; it is required when
	// such a task is registered.
	Locker BackgroundLocker
	Logger *slog.Logger
	// Now measures waits and durations; time.Now by default.
	Now func() time.Time
}

// backgroundLockTimeout bounds taking a cluster-wide lock, which includes
// waiting for a pooled connection.
const backgroundLockTimeout = 5 * time.Second

// Background is the job executor's lane for recurring background jobs
// (G41.1): webhook delivery, the watch statistics roll-up, the playback
// progress flush and subtitle OCR. It owns one goroutine per registered kind for the service
// lifetime; app.RunBackground paces it. Every run is admitted first: a
// cluster-wide kind takes its database advisory lock (another instance
// running it skips the run), a kind with a work class takes one permit of
// the shared budget for the run (a full budget skips it). Waits, durations
// and outcomes are kept per kind for the job metrics (G41.8).
type Background struct {
	opts BackgroundOptions

	mu      sync.Mutex
	tasks   []app.BackgroundTask
	started bool
	done    [domain.BackgroundJobKindCount]chan struct{}

	metricsMu sync.Mutex
	since     time.Time
	groups    [domain.BackgroundJobKindCount]app.BackgroundJobGroup
}

// NewBackground builds an empty lane.
func NewBackground(opts BackgroundOptions) (*Background, error) {
	if opts.Logger == nil {
		return nil, domain.ErrInvalid
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	b := &Background{opts: opts, since: opts.Now()}
	for i, kind := range domain.BackgroundJobKinds() {
		b.groups[i].Kind = kind
	}
	return b, nil
}

// Register adds a task before Start. Each kind registers once.
func (b *Background) Register(task app.BackgroundTask) error {
	if err := task.Validate(); err != nil {
		return err
	}
	if task.Cluster && b.opts.Locker == nil {
		return domain.ErrInvalid
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return errors.New("background jobs already started")
	}
	for _, t := range b.tasks {
		if t.Kind == task.Kind {
			return domain.ErrConflict
		}
	}
	b.tasks = append(b.tasks, task)
	return nil
}

// Kinds lists the registered kinds in registration order.
func (b *Background) Kinds() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	kinds := make([]string, len(b.tasks))
	for i, t := range b.tasks {
		kinds[i] = t.Kind
	}
	return kinds
}

// Start runs every registered task until ctx ends. ctx must be the service
// lifetime. A lane starts once.
func (b *Background) Start(ctx context.Context) error {
	if ctx == nil {
		return domain.ErrInvalid
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return errors.New("background jobs already started")
	}
	b.started = true
	for _, task := range b.tasks {
		i := domain.BackgroundJobKindIndex(task.Kind)
		done := make(chan struct{})
		b.done[i] = done
		go func() {
			defer close(done)
			app.RunBackground(ctx, task, b.runner(task, i))
		}()
	}
	return nil
}

// Wait blocks until the named kinds, or every kind when none is named, have
// returned after their context ended. Kinds not started return at once.
func (b *Background) Wait(kinds ...string) {
	b.mu.Lock()
	done := b.done
	b.mu.Unlock()
	if len(kinds) == 0 {
		all := domain.BackgroundJobKinds()
		kinds = all[:]
	}
	for _, kind := range kinds {
		if i := domain.BackgroundJobKindIndex(kind); i >= 0 && done[i] != nil {
			<-done[i]
		}
	}
}

func (b *Background) runner(task app.BackgroundTask, i int) app.BackgroundRunner {
	// G46.3: every record of a run, the task's own included, names the
	// kind. Background runs have no job row, so no taskId or jobRunId.
	fields := domain.LogFields{Kind: task.Kind}
	return func(stop, work context.Context, due time.Time) bool {
		stop, work = domain.WithLogFields(stop, fields), domain.WithLogFields(work, fields)
		release, outcome := b.admit(stop, task)
		if release == nil {
			b.count(i, outcome)
			b.opts.Logger.DebugContext(stop, "background job run not started", "component", "jobs", "outcome", outcome)
			return false
		}
		started := b.opts.Now()
		b.begin(i, started.Sub(due))
		again, err := task.Run(stop, work)
		release()
		outcome = domain.BackgroundRunSucceeded
		if err != nil {
			outcome = domain.BackgroundRunFailed
			if stop.Err() != nil {
				outcome = domain.BackgroundRunCancelled
			}
		}
		duration := b.opts.Now().Sub(started)
		b.end(i, outcome, duration)
		b.opts.Logger.DebugContext(stop, "background job run finished", "component", "jobs", "outcome", outcome, "durationMs", duration.Milliseconds())
		return again && err == nil
	}
}

// admit takes the cluster lock and the budget permit of one run. A nil
// release means the run does not start, for the returned outcome.
func (b *Background) admit(stop context.Context, task app.BackgroundTask) (func(), string) {
	if stop.Err() != nil {
		return nil, domain.BackgroundRunCancelled
	}
	unlock := func() {}
	if task.Cluster {
		lockCtx, cancel := context.WithTimeout(stop, backgroundLockTimeout)
		release, acquired, err := b.opts.Locker.TryBackgroundLock(lockCtx, task.Kind)
		cancel()
		switch {
		case stop.Err() != nil:
			if err == nil && acquired {
				release()
			}
			return nil, domain.BackgroundRunCancelled
		case err != nil:
			b.opts.Logger.WarnContext(stop, "background job lock unavailable", "component", "jobs", "kind", task.Kind, "code", "background_lock_unavailable")
			return nil, domain.BackgroundRunFailed
		case !acquired:
			return nil, domain.BackgroundRunSkipped
		}
		unlock = release
	}
	if task.Class == 0 || b.opts.Budget == nil {
		return unlock, ""
	}
	// Wait at most one interval: a later run takes over a saturated budget.
	wait, cancel := context.WithTimeout(stop, task.Interval)
	permit, err := b.opts.Budget.Acquire(wait, task.Class)
	cancel()
	if err != nil {
		unlock()
		if stop.Err() != nil {
			return nil, domain.BackgroundRunCancelled
		}
		return nil, domain.BackgroundRunSkipped
	}
	return func() { permit(); unlock() }, ""
}

func (b *Background) count(i int, outcome string) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	b.groups[i].Outcomes[outcomeIndex(outcome)]++
}

func (b *Background) begin(i int, wait time.Duration) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	b.groups[i].Running++
	observeBackground(&b.groups[i].Wait, wait)
}

func (b *Background) end(i int, outcome string, duration time.Duration) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	g := &b.groups[i]
	g.Running--
	g.Outcomes[outcomeIndex(outcome)]++
	observeBackground(&g.Duration, duration)
}

func outcomeIndex(outcome string) int {
	for i, o := range domain.BackgroundRunOutcomes() {
		if o == outcome {
			return i
		}
	}
	return 1 // failed
}

// observeBackground adds one sample; a sample equal to a bound belongs to
// that bucket, the last bucket is +Inf.
func observeBackground(h *app.JobMetricHistogram, d time.Duration) {
	seconds := max(d, 0).Seconds()
	bucket := len(h.BucketCounts) - 1
	for j, bound := range app.BackgroundJobBoundsSeconds() {
		if seconds <= bound {
			bucket = j
			break
		}
	}
	h.Count++
	h.SumSeconds += seconds
	h.BucketCounts[bucket]++
}

// BackgroundJobMetrics returns a copy of the metrics of every kind.
func (b *Background) BackgroundJobMetrics() app.BackgroundJobMetricsSnapshot {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return app.BackgroundJobMetricsSnapshot{StartedAt: b.since, Groups: b.groups}
}

var _ app.BackgroundJobMetricsSource = (*Background)(nil)
