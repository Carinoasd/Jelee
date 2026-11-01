//go:build !windows

package subtitleocr

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/MoYuanCN/Jelee/internal/platform/testutil"
)

// queueSource writes another source file and queues it under id.
func queueSource(t *testing.T, e env, id, name string) domain.ProbeSource {
	t.Helper()
	library := filepath.Dir(e.media)
	if err := os.WriteFile(filepath.Join(library, name), []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	source := domain.ProbeSource{RootPath: library, RelativePath: name}
	if _, err := e.service.Locate(context.Background(), id, source, 3); err != ErrPending {
		t.Fatalf("queue %s: %v", name, err)
	}
	return source
}

// The queue is dispatched by the subtitle_ocr background job: one source
// per run in queue order, outcomes on the lane's metrics.
func TestServiceTaskIsTheSubtitleOCRBackgroundJob(t *testing.T) {
	e := newEnv(t, Config{QueueSize: 4})
	task := e.service.Task()
	if task.Kind != domain.JobSubtitleOCR || !task.Cluster || !task.IdleWait || task.Interval != DispatchPoll || task.Wake == nil || task.Class != 0 || task.StopGrace != 0 || task.Validate() != nil {
		t.Fatalf("task %+v", task)
	}
	if (*Service)(nil).Task().Validate() == nil {
		t.Fatal("nil service has a task")
	}
	// Sources queued while one runs start in the order they were queued.
	e.extractor.block = make(chan struct{})
	if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return e.extractor.calls.Load() == 1 })
	queueSource(t, e, otherID, "b.mkv")
	queueSource(t, e, "00000000-0000-4000-8000-000000000003", "c.mkv")
	queueSource(t, e, "00000000-0000-4000-8000-000000000004", "d.mkv")
	if g := runs(e.lane); g.Running != 1 {
		t.Fatalf("running %d", g.Running)
	}
	close(e.extractor.block)
	stats := e.settled(t, 4)
	e.extractor.mu.Lock()
	order := slices.Clone(e.extractor.order)
	e.extractor.mu.Unlock()
	if !slices.Equal(order, []string{"Film.mkv", "b.mkv", "c.mkv", "d.mkv"}) || stats.Completed != 4 || stats.Queued != 0 {
		t.Fatalf("order %v stats %+v", order, stats)
	}
	waitFor(t, func() bool { return runs(e.lane).Running == 0 })
	if g := runs(e.lane); g.Outcomes[0] < 4 || g.Outcomes[1] != 0 || g.Wait.Count != g.Duration.Count {
		t.Fatalf("lane %+v", g)
	}

	// A failed job is a failed run; the next queued source still starts at
	// once although the lane does not repeat a failed run by itself.
	failing := newEnv(t, Config{})
	failing.extractor.errs = []error{matroska.ErrTooLarge}
	if _, err := failing.service.Locate(context.Background(), testSource, failing.source, 3); err != ErrPending {
		t.Fatal(err)
	}
	queueSource(t, failing, otherID, "b.mkv")
	waitFor(t, func() bool { s := failing.service.Stats(); return s.Failed == 1 && s.Completed == 1 })
	waitFor(t, func() bool { return runs(failing.lane).Running == 0 })
	if g := runs(failing.lane); g.Outcomes[1] != 1 || g.Outcomes[2] != 0 {
		t.Fatalf("lane %+v", g)
	}
}

// Two instances share the cluster-wide lock: only one recognizes at a
// time, the other's runs are skipped with its queue left as it was, and it
// takes over once the lock is free.
func TestServiceLaneOneInstanceAtATime(t *testing.T) {
	lock := &clusterLock{}
	fast := func(s *Service) { s.poll = 5 * time.Millisecond }
	envs := []env{newEnvOn(t, Config{}, lock, nil, fast), newEnvOn(t, Config{}, lock, nil, fast)}
	for _, e := range envs {
		e.recognizer.gate = make(chan struct{})
		if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool {
		return envs[0].recognizer.active.Load()+envs[1].recognizer.active.Load() == 1 && lock.denied.Load() >= 3
	})
	holder, waiting := envs[0], envs[1]
	if waiting.recognizer.active.Load() == 1 {
		holder, waiting = waiting, holder
	}
	time.Sleep(20 * time.Millisecond)
	if waiting.extractor.calls.Load() != 0 || waiting.recognizer.calls.Load() != 0 || holder.recognizer.active.Load() != 1 {
		t.Fatal("the second instance recognized while the first held the lock")
	}
	if s := waiting.service.Stats(); s.Queued != 1 || s.Running != 0 {
		t.Fatalf("waiting instance stats %+v", s)
	}
	if g := runs(waiting.lane); g.Outcomes[3] == 0 || g.Running != 0 {
		t.Fatalf("waiting lane %+v", g)
	}
	close(holder.recognizer.gate)
	if _, err := holder.locate(t, 3); err != nil {
		t.Fatal(err)
	}
	// Taking over, the second instance runs alone as well.
	waitFor(t, func() bool { return waiting.recognizer.active.Load() == 1 })
	if holder.recognizer.active.Load() != 0 {
		t.Fatal("both instances recognized")
	}
	close(waiting.recognizer.gate)
	if _, err := waiting.locate(t, 3); err != nil {
		t.Fatal(err)
	}
}

// Stopping the lane cancels the running job like Close did: staging and
// work files go, nothing is counted, no later source is dispatched, and
// no goroutine is left once the service closes (G29.3).
func TestServiceLaneStopCancelsAndDispatchesNothingMore(t *testing.T) {
	testutil.TrackGoroutines(t)
	e := newEnv(t, Config{})
	e.recognizer.gate = make(chan struct{})
	if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return e.recognizer.active.Load() == 1 })
	e.stopLane()
	if staging, _ := filepath.Glob(filepath.Join(e.cache, testSource, stagingPrefix+"*")); len(staging) != 0 {
		t.Fatal("staging left after the lane stopped")
	}
	if entries, _ := os.ReadDir(e.work); len(entries) != 0 {
		t.Fatal("work files left after the lane stopped")
	}
	if s := e.service.Stats(); s.Completed != 0 || s.Failed != 0 || s.Running != 0 {
		t.Fatalf("stats %+v", s)
	}
	if g := runs(e.lane); g.Outcomes[2] != 1 || g.Outcomes[1] != 0 || g.Running != 0 {
		t.Fatalf("lane %+v", g)
	}
	// A cancelled job is not a failure: it is not held back from a retry.
	e.service.mu.Lock()
	failures := len(e.service.failures)
	e.service.mu.Unlock()
	if failures != 0 {
		t.Fatal("cancelled job recorded as failed")
	}
	queueSource(t, e, otherID, "b.mkv")
	time.Sleep(30 * time.Millisecond)
	if e.extractor.calls.Load() != 1 || e.service.Stats().Queued != 1 {
		t.Fatalf("dispatched after stop: calls %d stats %+v", e.extractor.calls.Load(), e.service.Stats())
	}
}

// Every picture waits for a WorkCPU permit inside the run: the run holds
// none itself, its wait for the shared budget is part of its duration and
// shows as a waiting budget operation, and nothing is left held after.
func TestServiceBudgetWaitCountsInTheLaneMetrics(t *testing.T) {
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 4})
	if err != nil {
		t.Fatal(err)
	}
	held, err := budget.Acquire(context.Background(), app.WorkCPU)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvOn(t, Config{Budget: budget}, &clusterLock{}, budget)
	if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return budget.Stats().Waiting == 1 && runs(e.lane).Running == 1 })
	const blocked = 60 * time.Millisecond
	time.Sleep(blocked)
	if e.recognizer.calls.Load() != 0 || budget.Stats().CPU != 1 {
		t.Fatal("picture recognized without a permit")
	}
	held()
	if _, err := e.locate(t, 3); err != nil {
		t.Fatal(err)
	}
	e.settled(t, 1)
	waitFor(t, func() bool { return runs(e.lane).Running == 0 })
	g := runs(e.lane)
	if g.Duration.SumSeconds < blocked.Seconds() || g.Wait.Count != g.Duration.Count || g.Wait.SumSeconds >= blocked.Seconds() {
		t.Fatalf("lane %+v", g)
	}
	if budget.Stats() != (resources.Stats{}) {
		t.Fatalf("budget left %+v", budget.Stats())
	}
}
