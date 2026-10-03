package postgres

import (
	"errors"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"testing"
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
