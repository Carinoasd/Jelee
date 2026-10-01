package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// A heartbeat updates the parent twice in one transaction. A concurrent API
// read must wait for the jobs lock without holding the submitting user's row:
// PostgreSQL may check the unchanged actor foreign key on the second update.
func TestJobsLockOrderHeartbeatAndAuthorizedRead(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "heartbeat-read-order")
	l := f.claim(t, "heartbeat-owner")
	ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
	defer cancel()
	tx, err := f.s.jobTransaction(ctx)
	if err != nil {
		t.Fatal("heartbeat transaction", err)
	}
	defer tx.Rollback(context.Background())
	if _, err = fencedJob(ctx, tx, l); err != nil {
		t.Fatal("heartbeat lease", err)
	}
	if err = guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET lease_until=clock_timestamp()+$2*interval '1 microsecond' WHERE id=$1::uuid`, j.ID, time.Minute.Microseconds()); err != nil {
		t.Fatal("heartbeat first update", err)
	}
	type result struct {
		job domain.Job
		err error
	}
	read := make(chan result, 1)
	joined := false
	go func() {
		job, err := f.s.GetJob(ctx, f.a, j.ID)
		read <- result{job, err}
	}()
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
		if !joined {
			select {
			case <-read:
			case <-cleanup.Done():
				t.Error("authorized read did not join")
			}
		}
	}()
	// The barrier observes the real lock queue in this fixture's private schema;
	// it does not assume a scheduler delay or wait for the pre-fix user row lock.
	barrier, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err = f.s.Pool.QueryRow(barrier, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND classid=hashtext(current_schema())::oid AND objid=17481204 AND objsubid=2)`).Scan(&waiting); err != nil {
			t.Fatal("observe private jobs lock queue", err)
		}
		if waiting {
			break
		}
		select {
		case <-barrier.Done():
			t.Fatal("authorized read never reached the jobs lock")
		case got := <-read:
			joined = true
			t.Fatal("authorized read returned before heartbeat released its lock", got.err)
		case <-ticker.C:
		}
	}
	heartbeatErr := guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, j.ID)
	if heartbeatErr == nil {
		heartbeatErr = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	var got result
	select {
	case got = <-read:
		joined = true
	case <-ctx.Done():
		t.Fatal("authorized read did not complete after heartbeat", ctx.Err())
	}
	if heartbeatErr != nil || got.err != nil {
		t.Fatalf("heartbeat and authorized read must both succeed: heartbeat=%v read=%v", heartbeatErr, got.err)
	}
	if got.job.ID != j.ID || got.job.State != domain.JobRunning {
		t.Fatal("authorized read returned a different parent or state")
	}
	t.Log("observed own-schema jobs lock waiter; heartbeat second update and authorized read completed; reader joined")
}
