package postgres

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func commitAttemptReservation(p domain.NFOWritePreparation) domain.NFOWriteCommitAttemptReservation {
	return domain.NFOWriteCommitAttemptReservation{OriginalBytes: int64(len(p.Original)), ReplacementBytes: int64(len(p.Replacement)), OriginalHash: sha256.Sum256(p.Original), ReplacementHash: sha256.Sum256(p.Replacement), RetainedBytes: domain.NFOWriteCommitAttemptRetainedBytes(int64(len(p.Original)), int64(len(p.Replacement)))}
}
func commitAttemptFixture(t *testing.T) (jobFixture, domain.JobLease, domain.NFOWritePreparation, string, domain.NFOWriteCommitAttemptReservation) {
	t.Helper()
	f, l, p := nfoCommitFixture(t)
	r, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal("attempt journal unavailable")
	}
	if _, err = f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, r.Token, commitPlanFixture(p)); err != nil {
		t.Fatal("attempt first plan unavailable")
	}
	return f, l, p, r.Token, commitAttemptReservation(p)
}
func attemptSQLDenied(t *testing.T, err error, code, message string) {
	t.Helper()
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != code || (message != "" && denied.Message != message) {
		t.Fatal("attempt SQL denied for unrelated reason")
	}
}

func TestNFOCommitAttemptLedgerReplayAndBounds(t *testing.T) {
	f, l, p, token, reserve := commitAttemptFixture(t)
	before, err := f.s.GetNFOWriteCommitAttempts(f.ctx, l, 1, token)
	if err != nil || !before.ReservationRecorded || before.Reservation != reserve || before.Attempts != ([3]domain.NFOWriteCommitAttempt{}) {
		t.Fatal("legacy plan did not reserve exact bounded capacity")
	}
	var firstTime time.Time
	if f.s.Pool.QueryRow(f.ctx, `SELECT recorded_at FROM nfo_write_commit_attempt_reservations WHERE token=$1::uuid`, token).Scan(&firstTime) != nil {
		t.Fatal("reservation timestamp missing")
	}
	for i := 0; i < 3; i++ {
		if saved, err := f.s.ReserveNFOWriteCommitAttempts(f.ctx, l, 1, token, reserve); err != nil || saved != reserve {
			t.Fatal("reservation replay changed first intent")
		}
	}
	results := make(chan uint8, 12)
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() {
			n, err := f.s.AllocateNFOWriteCommitAttempt(f.ctx, l, 1, token, 0)
			results <- n
			failures <- err
		}()
	}
	for i := 0; i < 12; i++ {
		if <-results != 1 || <-failures != nil {
			t.Fatal("concurrent first attempt allocation diverged")
		}
	}
	ready := commitReadyFixture()
	ready.OutputIdentity[1] = p.NativeObservation.NFOFileIdentity()[1]
	ready.RollbackIdentity[1] = ready.OutputIdentity[1]
	first := domain.NFOWriteCommitFileCheckpoint{Phase: 1, OutputIdentity: ready.OutputIdentity}
	complete := domain.NFOWriteCommitFileCheckpoint{Phase: 2, OutputIdentity: ready.OutputIdentity, RollbackIdentity: ready.RollbackIdentity}
	if _, err = f.s.SaveNFOWriteCommitAttemptCheckpoint(f.ctx, l, 1, token, 1, complete); err == nil {
		t.Fatal("attempt full proof admitted without first phase")
	}
	if saved, err := f.s.SaveNFOWriteCommitAttemptCheckpoint(f.ctx, l, 1, token, 1, first); err != nil || saved != first {
		t.Fatal("attempt first proof failed")
	}
	if _, err = f.s.SaveNFOWriteCommitAttemptReady(f.ctx, l, 1, token, 1, ready); err == nil {
		t.Fatal("attempt partial ready admitted")
	}
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_attempts(token,attempt,previous_attempt) VALUES($1::uuid,3,NULL)`, token)
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "23514" || denied.ConstraintName != "nfo_attempt_previous_sequence" {
		t.Fatal("NULL predecessor bypassed bounded chain or unrelated rejection")
	}
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_attempts(token,attempt,previous_attempt) VALUES($1::uuid,3,2)`, token)
	if !errors.As(err, &denied) || denied.Code != "23503" || denied.ConstraintName != "nfo_attempt_previous_slot" {
		t.Fatal("attempt allocation skipped unsaved predecessor")
	}
	if n, err := f.s.AllocateNFOWriteCommitAttempt(f.ctx, l, 1, token, 1); err != nil || n != 2 {
		t.Fatal("second bounded attempt failed")
	}
	if v, err := f.s.SaveNFOWriteCommitAttemptCheckpoint(f.ctx, l, 1, token, 1, first); err == nil || v != (domain.NFOWriteCommitFileCheckpoint{}) {
		t.Fatal("older attempt writer admitted")
	}
	if _, err = f.s.SaveNFOWriteCommitAttemptCheckpoint(f.ctx, l, 1, token, 2, first); err == nil {
		t.Fatal("another attempt adopted retained first output")
	}
	second := complete
	second.OutputIdentity[16] += 10
	second.RollbackIdentity[16] += 10
	if _, err = f.s.SaveNFOWriteCommitAttemptCheckpoint(f.ctx, l, 1, token, 2, domain.NFOWriteCommitFileCheckpoint{Phase: 1, OutputIdentity: second.OutputIdentity}); err != nil {
		t.Fatal("second first output unavailable")
	}
	if _, err = f.s.SaveNFOWriteCommitAttemptCheckpoint(f.ctx, l, 1, token, 2, second); err != nil {
		t.Fatal("second complete proof unavailable")
	}
	pair := domain.NFOWriteCommitFilesReady{OutputIdentity: second.OutputIdentity, RollbackIdentity: second.RollbackIdentity}
	if v, err := f.s.SaveNFOWriteCommitAttemptReady(f.ctx, l, 1, token, 2, pair); err != nil || v != pair {
		t.Fatal("second attempt ready failed")
	}
	if n, err := f.s.AllocateNFOWriteCommitAttempt(f.ctx, l, 1, token, 2); err == nil || n != 0 {
		t.Fatal("new attempt allocated after retained ready")
	}
	if n, err := f.s.AllocateNFOWriteCommitAttempt(f.ctx, l, 1, token, 3); err == nil || n != 0 {
		t.Fatal("fourth attempt admitted")
	}
	fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal("attempt store reopen failed")
	}
	defer fresh.Pool.Close()
	actual, err := fresh.GetNFOWriteCommitAttempts(f.ctx, l, 1, token)
	if err != nil || actual.Reservation != reserve || actual.Attempts[0].Checkpoint != first || actual.Attempts[0].ReadyRecorded || !actual.Attempts[1].ReadyRecorded || actual.Attempts[1].Ready != pair || actual.Attempts[2] != (domain.NFOWriteCommitAttempt{}) {
		t.Fatal("reopen lost first or selected attempt evidence")
	}
	var kept int
	if f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_attempt_reservations WHERE token=$1::uuid AND recorded_at=$2`, token, firstTime).Scan(&kept) != nil || kept != 1 {
		t.Fatal("reservation first timestamp changed")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations`); err != nil {
		t.Fatal("attempt preparation TTL cleanup failed")
	}
	after, err := fresh.GetNFOWriteCommitAttempts(f.ctx, l, 1, token)
	if err != nil || after != actual {
		t.Fatal("TTL cleanup released attempt evidence")
	}
}

func TestNFOCommitAttemptEvidenceImmutable(t *testing.T) {
	f, l, _, token, reserve := commitAttemptFixture(t)
	changed := reserve
	changed.OriginalHash[0]++
	if saved, err := f.s.ReserveNFOWriteCommitAttempts(f.ctx, l, 1, token, changed); err == nil || saved != (domain.NFOWriteCommitAttemptReservation{}) {
		t.Fatal("changed first reservation admitted")
	}
	for _, query := range []string{`DELETE FROM nfo_write_commit_attempt_reservations WHERE token=$1::uuid`, `UPDATE nfo_write_commit_attempt_reservations SET original_hash=decode(repeat('00',32),'hex') WHERE token=$1::uuid`, `DELETE FROM nfo_write_commit_attempts WHERE token=$1::uuid`, `UPDATE nfo_write_commit_attempts SET previous_attempt=0 WHERE token=$1::uuid AND attempt=0`} {
		_, err := f.s.Pool.Exec(f.ctx, query, token)
		attemptSQLDenied(t, err, "23514", "nfo attempt evidence is immutable")
	}
	_, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_commit_attempt_quota_fence`)
	attemptSQLDenied(t, err, "23514", "nfo attempt quota fence retained")
}

func TestNFOCommitAttemptCatalogAfterConstraintFlush(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f, _, p, token, _ := commitAttemptFixture(t)
			tx, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal("owned attempt scope transaction unavailable")
			}
			defer tx.Rollback(f.ctx)
			if _, err = tx.Exec(f.ctx, `UPDATE nfo_write_commit_attempt_reservations SET token=token WHERE token=$1::uuid`, token); err != nil {
				t.Fatal("owned reservation replay failed")
			}
			if _, err = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
				t.Fatal("owned constraint flush failed")
			}
			_, err = tx.Exec(f.ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.LibraryID)
			attemptSQLDenied(t, err, "23514", "nfo catalog scope changed")
		})
	}
}

func TestNFOCommitAttemptLeaseAndActorFences(t *testing.T) {
	for _, reason := range []string{"expiry", "cancel", "disabled", "not_admin"} {
		t.Run(reason, func(t *testing.T) {
			f, l, _, token, reserve := commitAttemptFixture(t)
			var err error
			switch reason {
			case "expiry":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID)
			case "cancel":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l.Job.ID)
			case "disabled":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID)
			case "not_admin":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, f.a.UserID)
			}
			if err != nil {
				t.Fatal("owned attempt fence mutation failed")
			}
			if v, err := f.s.ReserveNFOWriteCommitAttempts(f.ctx, l, 1, token, reserve); err == nil || v != (domain.NFOWriteCommitAttemptReservation{}) {
				t.Fatal("nonlive reservation admitted")
			}
			if n, err := f.s.AllocateNFOWriteCommitAttempt(f.ctx, l, 1, token, 0); err == nil || n != 0 {
				t.Fatal("nonlive attempt allocation admitted")
			}
			if v, err := f.s.GetNFOWriteCommitAttempts(f.ctx, l, 1, token); err == nil || v != (domain.NFOWriteCommitAttempts{}) {
				t.Fatal("nonlive attempt observation admitted")
			}
			_, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_write_commit_attempt_reservations SET token=token WHERE token=$1::uuid`, token)
			message := "nfo attempt lease is not live"
			if reason == "disabled" || reason == "not_admin" {
				message = "nfo attempt actor is not active"
			}
			attemptSQLDenied(t, err, "23514", message)
		})
	}
}

func TestNFOCommitAttemptMigrationRetainsLegacyCapacity(t *testing.T) {
	f, l, p := nfoCommitFixture(t)
	jobMetricMigration(t, f, "down", 55)
	r, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal("legacy attempt journal unavailable")
	}
	if _, err = f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, r.Token, commitPlanFixture(p)); err != nil {
		t.Fatal("legacy attempt plan unavailable")
	}
	var firstTime time.Time
	if f.s.Pool.QueryRow(f.ctx, `SELECT recorded_at FROM nfo_write_commit_file_plans WHERE token=$1::uuid`, r.Token).Scan(&firstTime) != nil {
		t.Fatal("legacy plan timestamp missing")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),cancel_requested=true WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal("legacy stop failed")
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	var charged, slots int
	if f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_attempt_reservations WHERE token=$1::uuid AND recorded_at=$2 AND retained_bytes=$3`, r.Token, firstTime, commitAttemptReservation(p).RetainedBytes).Scan(&charged) != nil || charged != 1 {
		t.Fatal("migration lost first capacity evidence")
	}
	if f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_attempts WHERE token=$1::uuid AND attempt=0`, r.Token).Scan(&slots) != nil || slots != 1 {
		t.Fatal("migration inferred new attempt or lost legacy slot")
	}
	if _, _, err = Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("retained attempt capacity downgraded")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != SchemaVersion-1 || !dirty {
		t.Fatal("retained attempt downgrade lost dirty status")
	}
}

func TestNFOCommitAttemptLegacyEvidenceExclusive(t *testing.T) {
	for _, kind := range []string{"legacy_ready", "new_attempt", "legacy_partial"} {
		t.Run(kind, func(t *testing.T) {
			f, l, p, token, _ := commitAttemptFixture(t)
			pair := commitReadyFixture()
			pair.OutputIdentity[1] = p.NativeObservation.NFOFileIdentity()[1]
			pair.RollbackIdentity[1] = pair.OutputIdentity[1]
			if kind == "legacy_ready" {
				if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, token, pair); err != nil {
					t.Fatal("legacy first ready unavailable")
				}
				if n, err := f.s.AllocateNFOWriteCommitAttempt(f.ctx, l, 1, token, 0); err == nil || n != 0 {
					t.Fatal("attempt allocated after retained legacy ready")
				}
			} else {
				first := domain.NFOWriteCommitFileCheckpoint{Phase: 1, OutputIdentity: pair.OutputIdentity}
				if kind == "legacy_partial" {
					if _, err := f.s.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, token, first); err != nil {
						t.Fatal("legacy first checkpoint unavailable")
					}
				}
				if _, err := f.s.AllocateNFOWriteCommitAttempt(f.ctx, l, 1, token, 0); err != nil {
					t.Fatal("new attempt unavailable")
				}
				if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, token, pair); err == nil {
					t.Fatal("legacy ready bypassed allocated attempt")
				}
				if _, err := f.s.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, token, first); err == nil {
					t.Fatal("legacy checkpoint wrote after new attempt")
				}
				if kind == "legacy_partial" {
					if _, err := f.s.SaveNFOWriteCommitAttemptCheckpoint(f.ctx, l, 1, token, 1, first); err == nil {
						t.Fatal("new attempt adopted retained legacy output")
					}
				}
			}
		})
	}
}
