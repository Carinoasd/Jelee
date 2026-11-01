package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

type progressRepo struct {
	*executionFake
	read func(context.Context, domain.JobLease) (domain.JobProgress, error)
}

func (p progressRepo) ReadJobProgress(ctx context.Context, lease domain.JobLease) (domain.JobProgress, error) {
	return p.read(ctx, lease)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// routerRecords flushes the production router and returns the records whose
// message is msg.
func routerRecords(t *testing.T, router *logging.Router, out *lockedBuffer, msg string) []map[string]any {
	t.Helper()
	if err := router.Flush(); err != nil {
		t.Fatal(err)
	}
	var found []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var record map[string]any
		if line == "" || json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if record["msg"] == msg {
			found = append(found, record)
		}
	}
	return found
}

// G46.3 task log: a running job logs its progress after a heartbeat, at
// most once per interval, and the completion record repeats the last
// reading; every record of the claim carries taskId, jobRunId and kind
// through the job's log context and passes the production whitelist.
func TestRunnerLogsJobProgressWithTaskFields(t *testing.T) {
	const jobID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	f := oneJob(t)
	total, percent := int64(200), 25.0
	var reads atomic.Int32
	repo := progressRepo{executionFake: f.repo, read: func(c context.Context, l domain.JobLease) (domain.JobProgress, error) {
		checkDBDeadline(t, c)
		if l.Job.ID != jobID || l.Generation != 7 {
			t.Error("progress read lost the lease")
		}
		reads.Add(1)
		return domain.JobProgress{Done: 50, Total: &total, Percent: &percent, ETABasis: domain.JobETAThroughput}, nil
	}}
	out := &lockedBuffer{}
	router, err := logging.Open(logging.Options{}, out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Close() })
	opts := DefaultOptions()
	opts.Workers = 1
	clock := newTestClock()
	opts.Clock = clock
	entered, proceed := make(chan struct{}, 1), make(chan struct{})
	scan := scannerFunc(func(_ context.Context, _ domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
		entered <- struct{}{}
		<-proceed
		return emit(domain.ScanBatch{Done: true})
	})
	r, err := New(repo, scan, opts, router.Logger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopRunner(t, r) })
	startRunner(t, r)
	receive(t, entered)
	heartbeat := opts.LeaseDuration / 3
	if clock.fire(heartbeat) != 1 {
		t.Fatal("heartbeat timer missing")
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(routerRecords(t, router, out, "job progress")) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no progress record:\n%s", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A second heartbeat inside the interval does not log again.
	clock.waitFor(t, heartbeat, 1)
	clock.fire(heartbeat)
	clock.waitFor(t, heartbeat, 1)
	close(proceed)
	receive(t, f.terminal)
	clock.waitFor(t, opts.PollInterval, 1)
	progress := routerRecords(t, router, out, "job progress")
	if len(progress) != 1 || reads.Load() != 1 {
		t.Fatalf("progress records %d, reads %d", len(progress), reads.Load())
	}
	completed := routerRecords(t, router, out, "inventory job completed")
	started := routerRecords(t, router, out, "job started")
	if len(completed) != 1 || len(started) != 1 {
		t.Fatalf("records:\n%s", out.String())
	}
	for name, record := range map[string]map[string]any{"progress": progress[0], "completed": completed[0], "started": started[0]} {
		if record["taskId"] != jobID || record["jobRunId"] != jobID+":7" || record["kind"] != "inventory_scan" || record["component"] != "jobs" {
			t.Errorf("%s record fields %v", name, record)
		}
	}
	for name, record := range map[string]map[string]any{"progress": progress[0], "completed": completed[0]} {
		if record["count"] != float64(50) || record["total"] != float64(200) || record["progress"] != float64(25) || record["level"] != "INFO" {
			t.Errorf("%s record progress %v", name, record)
		}
	}
	if strings.Contains(out.String(), "private-root") {
		t.Fatal("root path logged")
	}
}

// A failed progress read is skipped: no record, and the lease stays.
func TestRunnerProgressReadFailureIsSkipped(t *testing.T) {
	clock := newTestClock()
	var heartbeats atomic.Int32
	repo := progressRepo{executionFake: &executionFake{heartbeat: func(context.Context, domain.JobLease, time.Duration) (bool, error) {
		heartbeats.Add(1)
		return false, nil
	}}, read: func(context.Context, domain.JobLease) (domain.JobProgress, error) {
		return domain.JobProgress{}, domain.ErrDatabase
	}}
	opts := DefaultOptions()
	opts.Clock = clock
	var output bytes.Buffer
	r, err := New(repo, scannerFunc(nil), opts, slog.New(slog.NewJSONHandler(&output, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	started, done := make(chan struct{}), make(chan struct{})
	var last domain.JobProgress
	go r.monitorProgress(ctx, domain.JobLease{}, cancel, started, done, &last)
	<-started
	heartbeat := opts.LeaseDuration / 3
	clock.waitFor(t, heartbeat, 1)
	clock.fire(heartbeat)
	clock.waitFor(t, heartbeat, 1)
	cancel(nil)
	<-done
	if heartbeats.Load() != 1 || context.Cause(ctx) != context.Canceled || last != (domain.JobProgress{}) || strings.Contains(output.String(), "job progress") {
		t.Fatalf("heartbeats %d cause %v last %+v output %s", heartbeats.Load(), context.Cause(ctx), last, output.String())
	}
}

func TestJobLogFieldsNameTheKind(t *testing.T) {
	lease := domain.JobLease{Job: domain.Job{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Kind: domain.JobNFOApply}, Generation: 3}
	if f := jobLogFields(lease); f.Kind != domain.JobNFOApply || f.JobRunID != lease.Job.ID+":3" || f.TaskID != lease.Job.ID {
		t.Fatalf("fields %+v", f)
	}
	lease.Job.Kind = ""
	if f := jobLogFields(lease); f.Kind != "inventory_scan" {
		t.Fatalf("legacy kind %+v", f)
	}
}

// G46.3: a background run's own records and the lane's run records carry
// the kind through the run's log context (background runs have no job row,
// hence no taskId or jobRunId), with the outcome and duration.
func TestBackgroundRunRecordsNameTheKind(t *testing.T) {
	out := &lockedBuffer{}
	router, err := logging.Open(logging.Options{Level: slog.LevelDebug}, out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Close() })
	locker := &fakeLocker{}
	b, err := NewBackground(BackgroundOptions{Locker: locker, Logger: router.Logger()})
	if err != nil {
		t.Fatal(err)
	}
	task := app.BackgroundTask{Kind: domain.JobWatchStatsAggregation, Interval: time.Minute, Cluster: true,
		Run: func(stop, work context.Context) (bool, error) {
			router.Logger().DebugContext(stop, "watch statistics aggregated", "component", "watch_stats", "count", 3)
			router.Logger().DebugContext(work, "work context record", "component", "watch_stats")
			return false, nil
		}}
	run := b.runner(task, domain.BackgroundJobKindIndex(task.Kind))
	ctx := context.Background()
	run(ctx, ctx, time.Now())
	locker.held[task.Kind] = true
	run(ctx, ctx, time.Now())
	for msg, want := range map[string]map[string]any{
		"watch statistics aggregated":    {"count": float64(3)},
		"work context record":            {},
		"background job run finished":    {"outcome": domain.BackgroundRunSucceeded},
		"background job run not started": {"outcome": domain.BackgroundRunSkipped},
	} {
		records := routerRecords(t, router, out, msg)
		if len(records) != 1 || records[0]["kind"] != task.Kind || records[0]["taskId"] != nil || records[0]["jobRunId"] != nil {
			t.Fatalf("%s: %v", msg, records)
		}
		for key, value := range want {
			if records[0][key] != value {
				t.Fatalf("%s: %s=%v", msg, key, records[0][key])
			}
		}
	}
	if finished := routerRecords(t, router, out, "background job run finished"); finished[0]["durationMs"] == nil || finished[0]["level"] != "DEBUG" {
		t.Fatalf("finished %v", finished)
	}
}
