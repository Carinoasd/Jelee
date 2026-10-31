package app

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// BackgroundTask is one recurring background job (G41.1): the work a
// subsystem does on its own schedule. The job executor
// (internal/platform/jobs.Background) owns its goroutine and admits each run
// (cluster-wide lock, shared budget, metrics); RunBackground paces it.
type BackgroundTask struct {
	// Kind is one of domain.BackgroundJobKinds.
	Kind string
	// Interval paces the runs. With IdleWait it is the idle wait after a run
	// and the first run starts at once (a poll); otherwise runs keep a fixed
	// cadence from start, the first after one interval, and ticks missed
	// while a run overran are dropped.
	Interval time.Duration
	IdleWait bool
	// Wake requests an early run; nil never wakes. With a fixed cadence,
	// WakeDelay postpones the run so a burst of wakes is handled together;
	// later wakes during the delay are absorbed. A poll wakes at once.
	Wake      <-chan struct{}
	WakeDelay time.Duration
	// Cluster runs on one instance at a time; it must match
	// domain.BackgroundJobClusterWide.
	Cluster bool
	// Class is the shared budget class one run holds from start to end;
	// zero holds none, for runs that admit each of their own operations.
	Class WorkClass
	// StopGrace lets a run in progress finish this long after the task stops
	// before its work context is cancelled too.
	StopGrace time.Duration
	// Run performs one run. stop is cancelled when the task stops: no new
	// work should start. work outlives it by StopGrace and carries the work
	// already started. again asks for another run at once (a full batch).
	Run func(stop, work context.Context) (again bool, err error)
}

// Validate checks a task against the whitelist and its own bounds.
func (t BackgroundTask) Validate() error {
	if domain.BackgroundJobKindIndex(t.Kind) < 0 || t.Run == nil || t.Interval <= 0 || t.WakeDelay < 0 || t.StopGrace < 0 ||
		t.IdleWait && t.WakeDelay != 0 || t.Cluster != domain.BackgroundJobClusterWide(t.Kind) || t.Class > WorkIO {
		return domain.ErrInvalid
	}
	return nil
}

// BackgroundRunner performs one admitted run of task for a run that became
// due at due and reports whether another is wanted at once. The executor
// wraps Run with its admission and metrics.
type BackgroundRunner func(stop, work context.Context, due time.Time) (again bool)

// RunBackground paces task until ctx ends. run performs each run; nil calls
// task.Run directly. When ctx ends no new run starts and the work context of
// a run in progress is cancelled StopGrace later. RunBackground returns once
// that run has returned.
func RunBackground(ctx context.Context, task BackgroundTask, run BackgroundRunner) {
	if run == nil {
		run = func(stop, work context.Context, _ time.Time) bool {
			again, _ := task.Run(stop, work)
			return again
		}
	}
	work, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWork()
	stop := context.AfterFunc(ctx, func() {
		if task.StopGrace <= 0 {
			cancelWork()
			return
		}
		timer := time.NewTimer(task.StopGrace)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancelWork()
		case <-work.Done():
		}
	})
	defer stop()
	if task.IdleWait {
		pollBackground(ctx, work, task, run)
		return
	}
	tickBackground(ctx, work, task, run)
}

func pollBackground(ctx, work context.Context, task BackgroundTask, run BackgroundRunner) {
	due := time.Now()
	for ctx.Err() == nil {
		if run(ctx, work, due) {
			due = time.Now()
			continue
		}
		timer := time.NewTimer(task.Interval)
		select {
		case <-ctx.Done():
		case <-task.Wake:
			due = time.Now()
		case due = <-timer.C:
		}
		timer.Stop()
	}
}

func tickBackground(ctx, work context.Context, task BackgroundTask, run BackgroundRunner) {
	ticker := time.NewTicker(task.Interval)
	defer ticker.Stop()
	var delay <-chan time.Time
	for {
		var due time.Time
		select {
		case <-ctx.Done():
			return
		case <-task.Wake:
			if task.WakeDelay > 0 {
				if delay == nil {
					delay = time.After(task.WakeDelay)
				}
				continue
			}
			due = time.Now()
		case due = <-delay:
			delay = nil
		case due = <-ticker.C:
		}
		// A tick or wake ready together with the stop starts nothing.
		if ctx.Err() != nil {
			return
		}
		for run(ctx, work, due) && ctx.Err() == nil {
			due = time.Now()
		}
	}
}

// BackgroundJobGroup is one background kind's metrics on this instance.
// Outcomes follow domain.BackgroundRunOutcomes. Wait is from the moment a
// run became due to its start (after the cluster lock and the budget);
// Duration is from start to end. Skipped runs have neither.
type BackgroundJobGroup struct {
	Kind     string
	Running  int64
	Outcomes [4]uint64
	Wait     JobMetricHistogram
	Duration JobMetricHistogram
}

// BackgroundJobMetricsSnapshot holds every background kind in the order of
// domain.BackgroundJobKinds, including kinds this instance does not run.
// The values are per process and start at StartedAt.
type BackgroundJobMetricsSnapshot struct {
	StartedAt time.Time
	Groups    [3]BackgroundJobGroup
}

// BackgroundJobMetricsSource reads the background job metrics from memory.
type BackgroundJobMetricsSource interface {
	BackgroundJobMetrics() BackgroundJobMetricsSnapshot
}

// BackgroundJobBoundsSeconds are the wait and duration bucket bounds of
// background jobs: the job wait bounds, whose first bucket is a tenth of a
// second, because a run normally takes well under a second.
func BackgroundJobBoundsSeconds() [12]float64 { return JobWaitBoundsSeconds() }
