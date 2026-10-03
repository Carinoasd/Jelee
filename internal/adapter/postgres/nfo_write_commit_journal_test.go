package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func persistNFOWriteCommitFixture(ctx context.Context, s *Store, l domain.JobLease, seq int) (nfoWriteCommitRecord, error) {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nfoWriteCommitRecord{}, err
	}
	defer tx.Rollback(ctx)
	r, err := recordNFOWriteCommit(ctx, tx, l, seq)
	if err != nil {
		return nfoWriteCommitRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nfoWriteCommitRecord{}, storageError(err)
	}
	return r, nil
}

func nfoCommitFixture(t *testing.T) (jobFixture, domain.JobLease, domain.NFOWritePreparation) {
	t.Helper()
	f, service, _, request := nfoWritePreparationFixture(t)
	saved, _, err := service.Prepare(f.ctx, f.a, "commit-source", request)
	if err != nil {
		t.Fatal(err)
	}
	j := nfoWriteJobFixture(t, f, saved, "commit-job", domain.JobPriorityManual)
	l := nfoWriteLeaseFixture(t, f, j.ID)
	return f, l, saved
}

func TestNFOWriteCommitJournalConcurrentReplayAndReopen(t *testing.T) {
	f, l, saved := nfoCommitFixture(t)
	type result struct {
		r   nfoWriteCommitRecord
		err error
	}
	results := make(chan result, 16)
	for i := 0; i < 16; i++ {
		go func() { r, err := persistNFOWriteCommitFixture(f.ctx, f.s, l, 1); results <- result{r, err} }()
	}
	var first nfoWriteCommitRecord
	for i := 0; i < 16; i++ {
		got := <-results
		if got.err != nil {
			t.Fatal("concurrent journal record", got.err)
		}
		if i == 0 {
			first = got.r
		} else if got.r != first {
			t.Fatal("replay changed journal identity")
		}
	}
	if !domain.ValidID(first.Token) || first.Owner != l.Owner || first.Generation != l.Generation || first.JobID != l.Job.ID || first.Sequence != 1 || first.RecordedAt.IsZero() || !first.LeaseUntil.After(first.RecordedAt) {
		t.Fatal("invalid journal identity")
	}
	encoded, _ := json.Marshal(first)
	formatted := fmt.Sprintf("%+v %#v", first, first)
	if string(encoded) != "{}" || strings.Contains(formatted, first.Token) || strings.Contains(formatted, l.Owner) || strings.Contains(formatted, l.Job.ID) {
		t.Fatal("private journal record leaked")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations`); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Pool.Close()
	again, err := persistNFOWriteCommitFixture(f.ctx, fresh, l, 1)
	if err != nil || again != first {
		t.Fatal("reopen changed durable token", err)
	}
	task, err := fresh.GetNFOWriteTask(f.ctx, l, 1)
	if err != nil || !bytes.Equal(task.Preparation.Original, saved.Original) || !bytes.Equal(task.Preparation.Replacement, saved.Replacement) {
		t.Fatal("journal lost job-owned bytes", err)
	}
	var count int
	if err := fresh.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate replay retained records", err)
	}
}

func TestNFOWriteCommitJournalLeaseActorAndCancellation(t *testing.T) {
	for _, test := range []string{"owner", "generation", "expiry", "cancel", "disabled", "not admin", "missing entry", "wrong actual kind", "zero sequence", "large sequence"} {
		t.Run(test, func(t *testing.T) {
			f, l, _ := nfoCommitFixture(t)
			seq := 1
			switch test {
			case "owner":
				l.Owner = "other-owner"
			case "generation":
				l.Generation++
			case "expiry":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := f.s.CancelJob(f.ctx, f.a, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID); err != nil {
					t.Fatal(err)
				}
			case "not admin":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, f.a.UserID); err != nil {
					t.Fatal(err)
				}
			case "missing entry":
				seq = 2
			case "wrong actual kind":
				other, err := f.s.RegisterLibrary(f.ctx, "scan-only", t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				j, _, err := f.s.SubmitJob(f.ctx, f.a, other.Library.ID, "scan-only", domain.JobPriorityManual, f.policy)
				if err != nil {
					t.Fatal(err)
				}
				l, err = f.s.ClaimJob(f.ctx, "scan-owner", false, time.Minute)
				if err != nil || l.Job.ID != j.ID {
					t.Fatal("scan fixture", err)
				}
				l.Job.Kind = domain.JobNFOWrite
			case "zero sequence":
				seq = 0
			case "large sequence":
				seq = 101
			}
			r, err := persistNFOWriteCommitFixture(f.ctx, f.s, l, seq)
			if err == nil || r != (nfoWriteCommitRecord{}) {
				t.Fatal("denied journal returned a record")
			}
			want := domain.ErrInvalid
			switch test {
			case "owner", "generation", "expiry":
				want = domain.ErrJobLeaseLost
			case "cancel":
				want = context.Canceled
			case "disabled", "not admin":
				want = domain.ErrForbidden
			}
			if !errors.Is(err, want) {
				t.Fatal("journal refused for unrelated reason", err)
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal`).Scan(&count); err != nil || count != 0 {
				t.Fatal("denied journal left data", err)
			}
		})
	}
}

func TestNFOWriteCommitJournalFinalLeaseAndTransactionRollback(t *testing.T) {
	for _, reason := range []string{"rollback", "expiry", "disable", "cancel"} {
		t.Run(reason, func(t *testing.T) {
			f, l, _ := nfoCommitFixture(t)
			if reason == "expiry" {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '500 milliseconds' WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := f.s.jobTransaction(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			if _, err := recordNFOWriteCommit(f.ctx, tx, l, 1); err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "rollback":
				_ = tx.Rollback(f.ctx)
			case "expiry":
				if _, err := tx.Exec(f.ctx, `SELECT pg_sleep(0.6)`); err != nil {
					t.Fatal(err)
				}
			case "disable":
				if _, err := tx.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := tx.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if reason != "rollback" {
				err = tx.Commit(f.ctx)
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "nfo commit journal lease is not live" {
					t.Fatal("final journal lease check bypassed", err)
				}
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed journal transaction retained record", err)
			}
		})
	}
}

func TestNFOWriteCommitJournalRetainsHistoryAndRejectsTransitions(t *testing.T) {
	f, l, saved := nfoCommitFixture(t)
	if _, err := persistNFOWriteCommitFixture(f.ctx, f.s, l, 1); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DELETE FROM nfo_write_commit_journal WHERE job_id=$1::uuid`,
		`UPDATE nfo_write_commit_journal SET token=gen_random_uuid() WHERE job_id=$1::uuid`,
		`DELETE FROM jobs WHERE id=$1::uuid`,
		`UPDATE jobs SET owner='replacement-owner' WHERE id=$1::uuid`,
		`UPDATE jobs SET generation=generation+1 WHERE id=$1::uuid`,
		`UPDATE jobs SET state='queued',owner=NULL,lease_until=NULL WHERE id=$1::uuid`,
		`UPDATE jobs SET state='succeeded',owner=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE id=$1::uuid`,
		`DELETE FROM nfo_write_entries WHERE job_id=$1::uuid`,
		`DELETE FROM nfo_write_requests WHERE job_id=$1::uuid`,
	} {
		_, err := f.s.Pool.Exec(f.ctx, query, l.Job.ID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" && pgErr.Code != "23503" {
			t.Fatal("pending journal mutation admitted or unrelated failure", err)
		}
	}
	// Even a forged input kind cannot use the generic release/refund path.
	for _, release := range []func(context.Context, domain.JobLease) error{f.s.ReleaseJob, f.s.PauseJob} {
		forged := l
		forged.Job.Kind = "inventory_scan"
		if err := release(f.ctx, forged); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("generic release accepted write job", err)
		}
	}
	if _, err := f.s.CancelJob(f.ctx, f.a, l.Job.ID); err != nil {
		t.Fatal("pending cancellation", err)
	}
	// Stopping does not certify filesystem outcome and must retain recovery data.
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',owner=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal("stop pending job", err)
	}
	for i := 0; i < 3; i++ {
		j := f.submit(t, fmt.Sprintf("ordinary-history-%d", i))
		if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := f.s.jobTransaction(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if err := trimJobs(f.ctx, tx, 1); err != nil {
		t.Fatal("history trim blocked ordinary jobs", err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	var journals, ordinary int
	var original, replacement []byte
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_commit_journal),(SELECT count(*) FROM jobs WHERE kind='inventory_scan'),original_bytes,replacement_bytes FROM nfo_write_entries WHERE job_id=$1::uuid`, l.Job.ID).Scan(&journals, &ordinary, &original, &replacement); err != nil || journals != 1 || ordinary != 1 || !bytes.Equal(original, saved.Original) || !bytes.Equal(replacement, saved.Replacement) {
		t.Fatal("trim discarded recovery bytes or ordinary retention", err)
	}
	for _, query := range []string{`DELETE FROM jobs WHERE id=$1::uuid`, `UPDATE jobs SET state='queued',finished_at=NULL WHERE id=$1::uuid`} {
		if _, err := f.s.Pool.Exec(f.ctx, query, l.Job.ID); err == nil {
			t.Fatal("stopped journal was deleted or requeued")
		}
	}
}

func TestNFOWriteCommitJournalMigrationEmptyAndRetained(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newJobFixture(t)
		before := jobMetricMigrationStorage(t, f)
		jobMetricMigration(t, f, "down", 48)
		jobMetricMigration(t, f, "up", SchemaVersion)
		if jobMetricMigrationStorage(t, f) != before || f.s.Ready(f.ctx) != nil {
			t.Fatal("journal round trip reset metrics or readiness")
		}
	})
	t.Run("retained", func(t *testing.T) {
		f, l, _ := nfoCommitFixture(t)
		// Exercise the published schema49 refusal independently of newer guards.
		jobMetricMigration(t, f, "down", 49)
		r, err := persistNFOWriteCommitFixture(f.ctx, f.s, l, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
			t.Fatal("downgrade removed unresolved journal")
		}
		version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
		if err != nil || version != 48 || !dirty || f.s.Ready(f.ctx) == nil {
			t.Fatal("retained journal refusal lost dirty state", err)
		}
		var token string
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT token::text FROM nfo_write_commit_journal WHERE job_id=$1::uuid`, l.Job.ID).Scan(&token); err != nil || token != r.Token {
			t.Fatal("refused downgrade lost journal", err)
		}
	})
}

func TestNFOWriteCommitJournalSQLGuardAndMissingTable(t *testing.T) {
	t.Run("raw owner is fenced", func(t *testing.T) {
		f, l, _ := nfoCommitFixture(t)
		_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner,recorded_at,lease_until) VALUES($1::uuid,1,$2,'forged-owner',clock_timestamp(),clock_timestamp()+interval '1 hour')`, l.Job.ID, l.Generation)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "nfo commit journal lease is not live" {
			t.Fatal("raw SQL bypassed live job binding", err)
		}
	})
	t.Run("raw actor is fenced", func(t *testing.T) {
		f, l, _ := nfoCommitFixture(t)
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID); err != nil {
			t.Fatal(err)
		}
		_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,1,$2,$3)`, l.Job.ID, l.Generation, l.Owner)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "nfo commit journal actor is not active" {
			t.Fatal("raw SQL bypassed actor binding", err)
		}
	})
	t.Run("current schema missing journal fails closed", func(t *testing.T) {
		f := newJobFixture(t)
		j := f.submit(t, "missing-table-history")
		if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
			t.Fatal(err)
		}
		tx, err := f.s.jobTransaction(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(f.ctx)
		if _, err := tx.Exec(f.ctx, `DROP TABLE nfo_write_commit_journal CASCADE`); err != nil {
			t.Fatal(err)
		}
		if err := trimJobs(f.ctx, tx, 0); err == nil {
			t.Fatal("current schema fell back without journal")
		}
		_ = tx.Rollback(f.ctx)
		var count int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs WHERE id=$1::uuid`, j.ID).Scan(&count); err != nil || count != 1 {
			t.Fatal("failed trim deleted history", err)
		}
	})
}

func TestNFOWriteCommitJournalStaleSnapshotsCannotTransferLease(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f, l, _ := nfoCommitFixture(t)
			stale, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer stale.Rollback(f.ctx)
			var count int
			if err := stale.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal`).Scan(&count); err != nil || count != 0 {
				t.Fatal("old journal snapshot", err)
			}
			// Exercise the SQL contract directly, without the Go helper's final
			// no-op job update accidentally providing the snapshot fence.
			if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,1,$2,$3)`, l.Job.ID, l.Generation, l.Owner); err != nil {
				t.Fatal(err)
			}
			_, err = stale.Exec(f.ctx, `UPDATE jobs SET owner='new-owner',generation=generation+1 WHERE id=$1::uuid`, l.Job.ID)
			if err == nil {
				err = stale.Commit(f.ctx)
			}
			if err == nil {
				t.Fatal("stale snapshot transferred unresolved commit lease")
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" && pgErr.Code != "40001" {
				t.Fatal("lease transfer refused for unrelated reason", err)
			}
			_ = stale.Rollback(f.ctx)
			var owner string
			var generation int64
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT owner,generation FROM jobs WHERE id=$1::uuid`, l.Job.ID).Scan(&owner, &generation); err != nil || owner != l.Owner || generation != l.Generation {
				t.Fatal("failed lease transfer changed owner", err)
			}
		})
	}
}

func TestNFOWriteCommitJournalGenericReleaseBeforeRecording(t *testing.T) {
	f, l, _ := nfoCommitFixture(t)
	l.Job.Kind = "inventory_scan"
	for _, release := range []func(context.Context, domain.JobLease) error{f.s.ReleaseJob, f.s.PauseJob} {
		if err := release(f.ctx, l); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("generic release accepted actual write kind before journal", err)
		}
	}
	j := f.get(t, l.Job.ID)
	if j.State != domain.JobRunning || j.Attempts != 1 {
		t.Fatal("generic release changed claim")
	}
}
