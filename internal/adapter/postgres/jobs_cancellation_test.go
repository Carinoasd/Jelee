package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestJobCancellationReadFencesWithoutWrites(t *testing.T) {
	f := newJobFixture(t)
	job := f.submit(t, "read-cancellation")
	lease := f.claim(t, "cancellation-owner")
	var before, after time.Time
	var oldVersion, newVersion string
	readVersion := func(dst *time.Time, version *string) {
		t.Helper()
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT lease_until,xmin::text FROM jobs WHERE id=$1::uuid`, job.ID).Scan(dst, version); err != nil {
			t.Fatal(err)
		}
	}
	readVersion(&before, &oldVersion)
	for i := 0; i < 5; i++ {
		if requested, err := f.s.ReadJobCancellation(f.ctx, lease); err != nil || requested {
			t.Fatal("live flag read", requested, err)
		}
	}
	readVersion(&after, &newVersion)
	if !before.Equal(after) || oldVersion != newVersion {
		t.Fatal("flag reads wrote or renewed job")
	}
	for _, mutate := range []func(*domain.JobLease){
		func(l *domain.JobLease) { l.Owner = "other-owner" },
		func(l *domain.JobLease) { l.Generation++ },
		func(l *domain.JobLease) { l.Job.ID = "invalid" },
	} {
		stale := lease
		mutate(&stale)
		if _, err := f.s.ReadJobCancellation(f.ctx, stale); !errors.Is(err, domain.ErrJobLeaseLost) {
			t.Fatal("unfenced cancellation read", err)
		}
	}
	cancelled, err := f.s.CancelJob(f.ctx, f.a, job.ID)
	if err != nil || !cancelled.CancelRequested {
		t.Fatal("authorized cancellation", err)
	}
	if requested, err := f.s.ReadJobCancellation(f.ctx, lease); err != nil || !requested {
		t.Fatal("committed cancellation not read", requested, err)
	}
	expired, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.s.ReadJobCancellation(expired, lease); !errors.Is(err, context.Canceled) {
		t.Fatal("read did not respect context", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReadJobCancellation(f.ctx, lease); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("expired owner read flag", err)
	}
}

func TestJobCancellationReadSeesOnlyCommittedFlag(t *testing.T) {
	f := newJobFixture(t)
	job := f.submit(t, "committed-cancellation")
	lease := f.claim(t, "committed-owner")
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	// This read uses a different pool connection and must neither block on the
	// provisional writer nor propagate an uncommitted flag.
	if requested, err := f.s.ReadJobCancellation(f.ctx, lease); err != nil || requested {
		t.Fatal("uncommitted flag escaped", requested, err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if requested, err := f.s.ReadJobCancellation(f.ctx, lease); err != nil || !requested {
		t.Fatal("committed flag missing", requested, err)
	}
	if err := f.s.ReleaseJob(f.ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReadJobCancellation(f.ctx, lease); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("terminal owner retained read authority", err)
	}
}
