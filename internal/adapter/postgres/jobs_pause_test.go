package postgres

import (
	"context"
	"errors"
	"github.com/MoYuanCN/Jelee/internal/domain"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobsPlannedPausePreservesCheckpointsAndFailureBudget(t *testing.T) {
	f := newJobFixture(t)
	job := f.submit(t, "planned-pause")
	// A real interrupted attempt must remain charged after future planned pauses.
	old := f.claim(t, "failed-worker")
	if err := f.s.ReleaseJob(f.ctx, old); err != nil {
		t.Fatal(err)
	}
	l := f.claim(t, "window-worker")
	d := f.directory(t, l)
	if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "movie.mkv", 7)}, Directories: []string{"child"}, Done: true}); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 6; round++ {
		if err := f.s.PauseJob(f.ctx, l); err != nil {
			t.Fatal(err)
		}
		got := f.get(t, job.ID)
		if got.State != domain.JobQueued || got.Attempts != 1 || got.Files != 1 || got.Bytes != 7 || got.FinishedAt != nil {
			t.Fatalf("pause changed progress or budget: %+v", got)
		}
		if err := f.s.PauseJob(f.ctx, l); !errors.Is(err, domain.ErrJobLeaseLost) {
			t.Fatalf("pause replay: %v", err)
		}
		previous := l
		l = f.claim(t, "window-worker")
		if l.Generation <= previous.Generation || l.Job.Attempts != 2 {
			t.Fatal("resume reused generation or lost failure count")
		}
		if err := f.s.PauseJob(f.ctx, previous); !errors.Is(err, domain.ErrJobLeaseLost) {
			t.Fatalf("stale pause: %v", err)
		}
		next := f.directory(t, l)
		if next.Path != "child" {
			t.Fatalf("completed root checkpoint lost: %q", next.Path)
		}
	}
	// Ordinary failures still exhaust the unchanged policy.
	if err := f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	l = f.claim(t, "last-attempt")
	if err := f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	got := f.get(t, job.ID)
	if got.State != domain.JobFailed || got.ErrorCode != "job_attempts_exhausted" || got.Attempts != 3 {
		t.Fatalf("failure limit weakened: %+v", got)
	}
}

func TestJobsPlannedPauseCancellationAndExpiredLease(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "cancelled"}[cancel], func(t *testing.T) {
			f := newJobFixture(t)
			job := f.submit(t, "pause")
			l := f.claim(t, "window-worker")
			if cancel {
				if _, err := f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
					t.Fatal(err)
				}
				if err := f.s.PauseJob(f.ctx, l); err != nil {
					t.Fatal(err)
				}
				got := f.get(t, job.ID)
				if got.State != domain.JobCancelled || got.FinishedAt == nil || got.Attempts != 1 {
					t.Fatalf("cancellation lost: %+v", got)
				}
			} else {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, job.ID); err != nil {
					t.Fatal(err)
				}
				if err := f.s.PauseJob(f.ctx, l); !errors.Is(err, domain.ErrJobLeaseLost) {
					t.Fatalf("expired pause: %v", err)
				}
				got := f.get(t, job.ID)
				if got.Attempts != 1 || got.State != domain.JobRunning {
					t.Fatal("expired owner changed job")
				}
			}
		})
	}
}

type pauseWorkWindow struct{ open atomic.Bool }

func (w *pauseWorkWindow) Allows(time.Time) bool { return w.open.Load() }

type pauseInventoryScanner struct{ entered chan struct{} }

func (s pauseInventoryScanner) ScanDirectory(ctx context.Context, d domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
	if d.Path == "." {
		if err := emit(domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "movie.mkv", 7)}, Directories: []string{"child"}, Done: true}); err != nil {
			return err
		}
		close(s.entered)
		<-ctx.Done()
		return ctx.Err()
	}
	return emit(domain.ScanBatch{Done: true})
}

func TestJobsWindowWorkerPausesAndResumesPostgres(t *testing.T) {
	f := newJobFixture(t)
	job := f.submit(t, "window-worker")
	window := &pauseWorkWindow{}
	window.open.Store(true)
	scanner := pauseInventoryScanner{entered: make(chan struct{})}
	opts := jobworker.DefaultOptions()
	opts.Workers = 1
	opts.PollInterval = 100 * time.Millisecond
	opts.Window = window
	r, err := jobworker.New(f.s, scanner, opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := r.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-scanner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not start")
	}
	window.open.Store(false)
	await := func(state string) domain.Job {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			got := f.get(t, job.ID)
			if got.State == state {
				return got
			}
			select {
			case <-deadline.C:
				t.Fatalf("state=%s want=%s code=%s", got.State, state, got.ErrorCode)
			case <-tick.C:
			}
		}
	}
	paused := await(domain.JobQueued)
	if paused.Attempts != 0 || paused.Files != 1 || paused.Bytes != 7 {
		t.Fatalf("paused progress: %+v", paused)
	}
	window.open.Store(true)
	completed := await(domain.JobSucceeded)
	if completed.Attempts != 1 || completed.Files != 1 || completed.Bytes != 7 {
		t.Fatalf("resumed progress: %+v", completed)
	}
}

func TestJobsPlannedPauseReleasesProbeLeaseAndResumesPhase(t *testing.T) {
	f := newProbeFixture(t)
	l, _ := f.begin(t, "pause-probe", "one.mkv")
	page, candidates := f.page(t, l)
	old, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	f.quota(t, 1, 1)
	if err = f.s.PauseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 0, 0)
	resumed := f.claim(t, "resumed-probe")
	next, candidates := f.page(t, resumed)
	if next.Token != page.Token || len(next.Entries) != 1 {
		t.Fatal("pause lost probe checkpoint")
	}
	if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &old, Metadata: probeTestMetadata()}}); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatalf("stale child committed: %v", err)
	}
	lease, err := f.s.AcquireProbe(f.ctx, resumed, next.Token, candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CommitProbeBatch(f.ctx, resumed, next.Token, []domain.ProbeCompletion{{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}}); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 1, 0)
	f.finish(t, resumed)
	if f.get(t, l.Job.ID).State != domain.JobSucceeded {
		t.Fatal("probe phase did not finish after pause")
	}
}

func TestJobsPlannedPausePreservesNFOCheckpoint(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "pause-nfo", "one.nfo", "two.nfo")
	completed := f.parseHead(t, l, nfoValidSummary())
	before, err := f.s.NextNFOPage(f.ctx, l, domain.NFOPageMax)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.PauseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	resumed := f.claim(t, "resumed-nfo")
	after, err := f.s.NextNFOPage(f.ctx, resumed, domain.NFOPageMax)
	if err != nil {
		t.Fatal(err)
	}
	if after.Token != before.Token || len(after.Entries) != 1 || after.Entries[0].Inventory.ID != before.Entries[0].Inventory.ID {
		t.Fatal("NFO checkpoint replayed completed work")
	}
	summary := nfoValidSummary()
	if _, err = f.s.CommitNFOBatch(f.ctx, l, before.Token, []domain.NFOCompletion{{Candidate: nfoCandidate(before.Entries[0]), Kind: domain.NFOCompletionParsed, Summary: &summary}}); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatalf("old NFO owner committed: %v", err)
	}
	final := f.parseHead(t, resumed, nfoValidSummary())
	if completed.Progress.Processed != 1 || final.Progress.Processed != 2 || final.Progress.Valid != 2 {
		t.Fatal("NFO counts duplicated after resume")
	}
	f.finish(t, resumed)
	if f.get(t, l.Job.ID).State != domain.JobSucceeded {
		t.Fatal("NFO phase did not complete")
	}
}
