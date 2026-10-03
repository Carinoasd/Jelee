package postgres

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// settlementFixture opens a journal with a synthetic legacy ready pair. These
// storage contract tests grant no filesystem authority and touch no files.
func settlementFixture(t *testing.T) (jobFixture, domain.JobLease, domain.NFOWritePreparation, string) {
	t.Helper()
	f, l, p := nfoCommitFixture(t)
	r, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal("settlement journal unavailable")
	}
	if _, err = f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, r.Token, commitPlanFixture(p)); err != nil {
		t.Fatal("settlement plan unavailable")
	}
	return f, l, p, r.Token
}

func settlementReadyFixture(t *testing.T, f jobFixture, l domain.JobLease, p domain.NFOWritePreparation, token string) {
	t.Helper()
	pair := commitReadyFixture()
	pair.OutputIdentity[1] = p.NativeObservation.NFOFileIdentity()[1]
	pair.RollbackIdentity[1] = pair.OutputIdentity[1]
	if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, token, pair); err != nil {
		t.Fatal("settlement ready unavailable")
	}
}

func settlementPhases(t *testing.T, f jobFixture, token string) []int {
	t.Helper()
	rows, err := f.s.Pool.Query(f.ctx, `SELECT phase FROM nfo_write_commit_settlements WHERE token=$1::uuid ORDER BY phase`, token)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var phases []int
	for rows.Next() {
		var phase int
		if err := rows.Scan(&phase); err != nil {
			t.Fatal(err)
		}
		phases = append(phases, phase)
	}
	return phases
}

func TestNFOCommitSettlementForwardOnlyAndImmutable(t *testing.T) {
	f, l, p, token := settlementFixture(t)
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitBackedUp); err == nil {
		t.Fatal("settlement admitted without ready evidence")
	}
	settlementReadyFixture(t, f, l, p, token)
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitReplaced); err == nil {
		t.Fatal("replacement admitted without a backup phase")
	}
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 1, domain.NFOWriteCommitBackedUp); err == nil {
		t.Fatal("settlement admitted for an attempt without ready")
	}
	first, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitBackedUp)
	if err != nil || first.Phase != domain.NFOWriteCommitBackedUp || first.BackedUpAt.IsZero() || !first.ReadyRecorded || first.ReadyAttempt != 0 {
		t.Fatal("backup phase unavailable", err)
	}
	again, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitBackedUp)
	if err != nil || !again.BackedUpAt.Equal(first.BackedUpAt) {
		t.Fatal("backup replay changed first time", err)
	}
	replaced, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitReplaced)
	if err != nil || replaced.Phase != domain.NFOWriteCommitReplaced || !replaced.BackedUpAt.Equal(first.BackedUpAt) || replaced.SettledAt.IsZero() {
		t.Fatal("replacement phase unavailable", err)
	}
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitRolledBack); err == nil {
		t.Fatal("rollback admitted after replacement")
	}
	if got, err := f.s.GetNFOWriteCommitSettlement(f.ctx, l, 1, token); err != nil || got != replaced {
		t.Fatal("settlement observation differs", err)
	}
	for _, query := range []string{`DELETE FROM nfo_write_commit_settlements`, `UPDATE nfo_write_commit_settlements SET recorded_at=recorded_at-interval '1 day'`, `UPDATE nfo_write_commit_settlements SET attempt=1`} {
		if _, err := f.s.Pool.Exec(f.ctx, query); err == nil {
			t.Fatal("settlement mutation accepted")
		}
	}
	var files, skipped int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT files,skipped FROM jobs WHERE id=$1::uuid`, l.Job.ID).Scan(&files, &skipped); err != nil || files != 1 || skipped != 0 {
		t.Fatal("job progress not derived from settlements", err)
	}
}

func TestNFOCommitSettlementRollbackIsTerminal(t *testing.T) {
	f, l, p, token := settlementFixture(t)
	settlementReadyFixture(t, f, l, p, token)
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitBackedUp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitRolledBack); err != nil {
		t.Fatal("rollback after backup refused", err)
	}
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitReplaced); err == nil {
		t.Fatal("replacement admitted after rollback")
	}
	if phases := settlementPhases(t, f, token); len(phases) != 2 || phases[0] != 1 || phases[1] != 3 {
		t.Fatal("rollback phases differ")
	}
}

func TestNFOCommitSettlementLeaseActorCatalogFences(t *testing.T) {
	for _, test := range []string{"expired", "cancel", "disabled", "not_admin", "foreign_owner", "catalog", "catalog_rollback", "direct_sql"} {
		t.Run(test, func(t *testing.T) {
			f, l, p, token := settlementFixture(t)
			settlementReadyFixture(t, f, l, p, token)
			phase := domain.NFOWriteCommitBackedUp
			want := domain.ErrInvalid
			switch test {
			case "expired":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				want = domain.ErrJobLeaseLost
			case "cancel":
				if _, err := f.s.CancelJob(f.ctx, f.a, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				want = context.Canceled
			case "disabled", "not_admin":
				column := map[string]string{"disabled": "disabled=true", "not_admin": "is_admin=false"}[test]
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET `+column+` WHERE id=$1::uuid`, f.a.UserID); err != nil {
					t.Fatal(err)
				}
				want = domain.ErrForbidden
			case "foreign_owner":
				l.Owner = "other-owner"
				want = domain.ErrJobLeaseLost
			case "catalog", "catalog_rollback":
				if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, p.Scope.ItemID, p.Scope.Revision+1); err != nil {
					t.Fatal(err)
				}
				want = domain.ErrConflict
				if test == "catalog_rollback" {
					phase, want = domain.NFOWriteCommitRolledBack, nil
				}
			case "direct_sql":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_settlements(token,phase,attempt,recorded_at,lease_until) VALUES($1::uuid,1,0,clock_timestamp(),clock_timestamp()+interval '1 minute')`, token)
				attemptSQLDenied(t, err, "23514", "nfo commit settlement lease is not live")
				return
			}
			_, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, phase)
			if want == nil {
				// Catalog drift cannot strand a rollback of the prepared original.
				if err != nil {
					t.Fatal("rollback refused after catalog drift", err)
				}
				return
			}
			if !errors.Is(err, want) {
				t.Fatal("settlement refused for an unrelated reason", err)
			}
			if phases := settlementPhases(t, f, token); len(phases) != 0 {
				t.Fatal("refused settlement retained a row")
			}
		})
	}
}

func TestNFOCommitSettlementCancelledRecoveryOnlyRollsBack(t *testing.T) {
	for _, backedUp := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "backed_up"}[backedUp], func(t *testing.T) {
			f, l, p, token := settlementFixture(t)
			settlementReadyFixture(t, f, l, p, token)
			if backedUp {
				if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitBackedUp); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.CancelJob(f.ctx, f.a, l.Job.ID); err != nil {
				t.Fatal(err)
			}
			resolved, err := f.s.FinishNFOWriteJob(f.ctx, l, domain.JobCancelled, "")
			if err != nil || resolved == backedUp {
				t.Fatal("cancelled stop resolution differs", resolved, err)
			}
			if !backedUp {
				return
			}
			recovery, err := f.s.AcquireNFOWriteCommitRecovery(f.ctx, l.Job.ID, "recovery-a", time.Minute)
			if err != nil || !recovery.Job.CancelRequested {
				t.Fatal("cancelled recovery lease unavailable", err)
			}
			for _, phase := range []domain.NFOWriteCommitSettlementPhase{domain.NFOWriteCommitReplaced, domain.NFOWriteCommitBackedUp} {
				if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, recovery, 1, token, 0, phase); err == nil {
					t.Fatal("cancelled recovery advanced the write")
				}
			}
			_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_settlements(token,phase,attempt,recorded_at,lease_until) VALUES($1::uuid,2,0,clock_timestamp(),clock_timestamp()+interval '1 minute')`, token)
			attemptSQLDenied(t, err, "23514", "nfo commit recovery cannot replace for a cancelled job")
			if _, err := f.s.CompleteNFOWriteCommitRecovery(f.ctx, recovery); err == nil {
				t.Fatal("recovery resolved a token between backup and rollback")
			}
			if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, recovery, 1, token, 0, domain.NFOWriteCommitRolledBack); err != nil {
				t.Fatal("cancelled recovery could not roll back", err)
			}
			state, err := f.s.CompleteNFOWriteCommitRecovery(f.ctx, recovery)
			if err != nil || state != domain.JobCancelled {
				t.Fatal("cancelled recovery did not resolve", state, err)
			}
		})
	}
}

func TestNFOCommitSettlementJobExitRequiresReplacement(t *testing.T) {
	f, l, p, token := settlementFixture(t)
	settlementReadyFixture(t, f, l, p, token)
	if _, err := f.s.FinishNFOWriteJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("success admitted before replacement", err)
	}
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitBackedUp); err != nil {
		t.Fatal(err)
	}
	// The trigger refuses success even without the application check.
	_, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='succeeded',owner=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE id=$1::uuid`, l.Job.ID)
	attemptSQLDenied(t, err, "23514", "nfo commit requires recovery before job transition")
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_resolutions(job_id,owner,epoch,recorded_at) VALUES($1::uuid,$2,0,clock_timestamp())`, l.Job.ID, l.Owner)
	attemptSQLDenied(t, err, "23514", "nfo commit settlement is unresolved")
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_native_claims`); err == nil {
		t.Fatal("unresolved physical claims deleted")
	}
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitReplaced); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.s.FinishNFOWriteJob(f.ctx, l, domain.JobSucceeded, "")
	if err != nil || !resolved {
		t.Fatal("replaced job did not succeed", err)
	}
	var state string
	var claims, resolutions int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT j.state,(SELECT count(*) FROM nfo_write_native_claims),(SELECT count(*) FROM nfo_write_commit_resolutions WHERE job_id=j.id AND epoch=0) FROM jobs j WHERE j.id=$1::uuid`, l.Job.ID).Scan(&state, &claims, &resolutions); err != nil || state != domain.JobSucceeded || claims != 0 || resolutions != 1 {
		t.Fatal("resolved job state differs", state, claims, resolutions, err)
	}
	// A resolution closes every lease and is immutable.
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='failed' WHERE id=$1::uuid`, l.Job.ID); err == nil {
		t.Fatal("resolved job left succeeded")
	}
	for _, query := range []string{`DELETE FROM nfo_write_commit_resolutions`, `UPDATE nfo_write_commit_resolutions SET epoch=1`} {
		if _, err := f.s.Pool.Exec(f.ctx, query); err == nil {
			t.Fatal("resolution mutation accepted")
		}
	}
	var live *time.Time
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT nfo_commit_live_lease(current_schema(),$1::uuid,$2,$3)`, l.Job.ID, l.Owner, l.Generation).Scan(&live); err != nil || live != nil {
		t.Fatal("resolved job kept a live lease", err)
	}
}

func TestNFOCommitSettlementRecoveryCompletion(t *testing.T) {
	for _, outcome := range []string{"replaced", "rolled_back", "unready"} {
		t.Run(outcome, func(t *testing.T) {
			f, l, p, token := settlementFixture(t)
			if outcome != "unready" {
				settlementReadyFixture(t, f, l, p, token)
				if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitBackedUp); err != nil {
					t.Fatal(err)
				}
			}
			// The original worker dies; the claim sweep stops the journaled job.
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.ClaimJob(f.ctx, "sweeper", false, time.Minute); err != nil && !errors.Is(err, domain.ErrNotFound) {
				t.Fatal(err)
			}
			pending, err := f.s.ListNFOWriteCommitRecoveries(f.ctx, 10)
			if err != nil || len(pending) != 1 || pending[0] != l.Job.ID {
				t.Fatal("stopped job not listed for recovery", err)
			}
			recovery, err := f.s.AcquireNFOWriteCommitRecovery(f.ctx, l.Job.ID, "recovery-a", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if listed, _ := f.s.ListNFOWriteCommitRecoveries(f.ctx, 10); len(listed) != 0 {
				t.Fatal("held recovery listed again")
			}
			want := domain.JobFailed
			switch outcome {
			case "replaced":
				if _, err := f.s.CompleteNFOWriteCommitRecovery(f.ctx, recovery); err == nil {
					t.Fatal("recovery resolved before a terminal phase")
				}
				if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, recovery, 1, token, 0, domain.NFOWriteCommitReplaced); err != nil {
					t.Fatal("recovery could not confirm the replacement", err)
				}
				want = domain.JobSucceeded
			case "rolled_back":
				if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, recovery, 1, token, 0, domain.NFOWriteCommitRolledBack); err != nil {
					t.Fatal(err)
				}
			}
			state, err := f.s.CompleteNFOWriteCommitRecovery(f.ctx, recovery)
			if err != nil || state != want {
				t.Fatal("recovery completion state differs", state, err)
			}
			var stored, code string
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT state,error_code FROM jobs WHERE id=$1::uuid`, l.Job.ID).Scan(&stored, &code); err != nil || stored != want || (want == domain.JobSucceeded) != (code == "") {
				t.Fatal("stored recovery state differs", stored, code, err)
			}
			if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, recovery, 1, token, 0, domain.NFOWriteCommitRolledBack); err == nil {
				t.Fatal("resolved recovery still wrote evidence")
			}
			if listed, _ := f.s.ListNFOWriteCommitRecoveries(f.ctx, 10); len(listed) != 0 {
				t.Fatal("resolved job listed for recovery")
			}
		})
	}
}

// settlementMigration finds the settlement migration by name, not number, so
// renumbering on merge needs no test change.
func settlementMigration(t *testing.T, suffix string) string {
	t.Helper()
	matches, err := fs.Glob(migrationFiles, "migrations/*_nfo_commit_settlements."+suffix+".sql")
	if err != nil || len(matches) != 1 {
		t.Fatal("settlement migration missing")
	}
	return strings.TrimPrefix(matches[0], "migrations/")
}

func TestNFOCommitSettlementMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	jobMetricMigration(t, f, "down", SchemaVersion-1)
	var removed, restored bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('nfo_write_commit_settlements') IS NULL AND to_regclass('nfo_write_commit_resolutions') IS NULL AND to_regprocedure('guard_nfo_commit_settlement()') IS NULL`).Scan(&removed); err != nil || !removed {
		t.Fatal("empty downgrade left settlement objects", err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT position('nfo_write_commit_resolutions' IN prosrc)=0 FROM pg_proc WHERE oid='retain_nfo_write_commit_job()'::regprocedure`).Scan(&restored); err != nil || !restored {
		t.Fatal("downgrade did not restore the retained job guard", err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("round-trip schema is not ready", err)
	}
}

func TestNFOCommitSettlementMigrationRefusesRetainedData(t *testing.T) {
	f, l, p, token := settlementFixture(t)
	settlementReadyFixture(t, f, l, p, token)
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, l, 1, token, 0, domain.NFOWriteCommitBackedUp); err != nil {
		t.Fatal(err)
	}
	nfoMigrationDenied(t, f, settlementMigration(t, "down"))
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("retained settlement downgraded")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != SchemaVersion-1 || !dirty {
		t.Fatal("refused settlement downgrade lost dirty status", err)
	}
	if phases := settlementPhases(t, f, token); len(phases) != 1 {
		t.Fatal("refused downgrade removed settlements")
	}
}
