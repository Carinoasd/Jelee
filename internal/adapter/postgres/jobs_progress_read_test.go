package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// G46.3: the task log reads a claimed job's progress with one fenced
// lookup that neither writes nor renews the job, and refuses stale owners.
func TestJobProgressReadFencesWithoutWrites(t *testing.T) {
	f := newJobFixture(t)
	job := f.submit(t, "read-progress")
	lease := f.claim(t, "progress-owner")
	var before, after time.Time
	var oldVersion, newVersion string
	readVersion := func(dst *time.Time, version *string) {
		t.Helper()
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT lease_until,xmin::text FROM jobs WHERE id=$1::uuid`, job.ID).Scan(dst, version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET directories=3 WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	readVersion(&before, &oldVersion)
	for i := 0; i < 3; i++ {
		progress, err := f.s.ReadJobProgress(f.ctx, lease)
		if err != nil || progress.Done != 3 || progress.ETABasis == "" || !domain.ValidJobProgress(progress) {
			t.Fatalf("progress %+v: %v", progress, err)
		}
	}
	readVersion(&after, &newVersion)
	if !before.Equal(after) || oldVersion != newVersion {
		t.Fatal("progress reads wrote or renewed the job")
	}
	for _, mutate := range []func(*domain.JobLease){
		func(l *domain.JobLease) { l.Owner = "other-owner" },
		func(l *domain.JobLease) { l.Generation++ },
		func(l *domain.JobLease) { l.Job.ID = "invalid" },
	} {
		stale := lease
		mutate(&stale)
		if _, err := f.s.ReadJobProgress(f.ctx, stale); !errors.Is(err, domain.ErrJobLeaseLost) {
			t.Fatal("unfenced progress read", err)
		}
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReadJobProgress(f.ctx, lease); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("expired owner read progress", err)
	}
}
