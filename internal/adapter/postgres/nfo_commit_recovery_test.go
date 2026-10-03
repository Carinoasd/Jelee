package postgres

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func stopNFOCommitJob(t *testing.T, f jobFixture, l domain.JobLease, state string) {
	t.Helper()
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state=$2,owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),cancel_requested=$2='cancelled' WHERE id=$1::uuid`, l.Job.ID, state); err != nil {
		t.Fatal("owned job stop failed")
	}
}

func TestNFOCommitRecoveryLeaseContinuesStoppedJob(t *testing.T) {
	for _, state := range []string{"failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			f, l, p := nfoCommitFixture(t)
			record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
			if err != nil {
				t.Fatal("owned recovery journal unavailable")
			}
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
			stage := func(lease domain.JobLease, repository app.NFOWriteCommitAttemptStageRepository) error {
				source, err := nfo.ReadSource(f.ctx, p.Scope.Source.RootPath, p.Scope.Source.RelativePath, p.Request.MaxBytes)
				if err != nil {
					t.Fatal("owned recovery source unavailable")
				}
				writer, _ := nfo.NewWriterWithBudget(budget)
				r := record
				return writer.StageCommitFiles(f.ctx, source, lease, r, repository)
			}
			// The original worker stops after creating names, before any checkpoint.
			if err := stage(l, lostFirstCheckpointStore{f.s}); !errors.Is(err, nfo.ErrReplace) {
				t.Fatal("lost first checkpoint not reported as unknown")
			}
			if _, err := f.s.AcquireNFOWriteCommitRecovery(f.ctx, l.Job.ID, "recovery-a", time.Minute); err == nil {
				t.Fatal("recovery admitted beside a running original lease")
			}
			stopNFOCommitJob(t, f, l, state)
			if err := stage(l, f.s); err == nil {
				t.Fatal("stopped original lease still staged")
			}
			first, err := f.s.AcquireNFOWriteCommitRecovery(f.ctx, l.Job.ID, "recovery-a", time.Minute)
			if err != nil || first.RecoveryEpoch != 1 || first.Generation != l.Generation || first.Owner != "recovery-a" || !first.ExpiresAt.After(time.Now().Add(-time.Second)) {
				t.Fatal("recovery lease unavailable")
			}
			if _, err := f.s.AcquireNFOWriteCommitRecovery(f.ctx, l.Job.ID, "recovery-b", time.Minute); !errors.Is(err, domain.ErrConflict) {
				t.Fatal("live recovery lease replaced")
			}
			// Recovery reopens the same token; it never starts another journal.
			again, err := f.s.BeginNFOWriteCommit(f.ctx, first, 1)
			if err != nil || again.Token != record.Token {
				t.Fatal("recovery did not reopen the existing token")
			}
			if state == "cancelled" {
				// Cancellation stands: recovery may settle or roll back retained
				// evidence later, but it never advances a cancelled write.
				if err := stage(first, f.s); err == nil {
					t.Fatal("recovery advanced a cancelled job")
				}
				attempts, err := f.s.GetNFOWriteCommitAttempts(f.ctx, first, 1, record.Token)
				if err != nil || attempts.Attempts[0].Number != 0 {
					t.Fatal("cancelled recovery allocated a new attempt")
				}
				if _, err := f.s.AllocateNFOWriteCommitAttempt(f.ctx, first, 1, record.Token, 0); err == nil {
					t.Fatal("cancelled recovery allocation admitted")
				}
				return
			}
			if err := stage(first, f.s); err != nil {
				t.Fatal("recovery lease did not finish the stopped token")
			}
			attempts, err := f.s.GetNFOWriteCommitAttempts(f.ctx, first, 1, record.Token)
			if err != nil || attempts.Attempts[0].Number != 1 || !attempts.Attempts[0].ReadyRecorded || attempts.Attempts[1].Number != 0 {
				t.Fatal("recovery did not select a fresh attempt")
			}
			if err := stage(first, f.s); err != nil {
				t.Fatal("recovery replay failed")
			}
			directory := filepath.Dir(filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath)))
			if _, err := os.Lstat(filepath.Join(directory, ".jelee-nfo-commit-"+strings.ReplaceAll(record.Token, "-", "")+"-output")); err != nil {
				t.Fatal("unknown legacy object removed by recovery")
			}
			var journals int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal WHERE job_id=$1::uuid`, l.Job.ID).Scan(&journals); err != nil || journals != 1 {
				t.Fatal("recovery opened another journal")
			}
			if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
				t.Fatal("retained recovery lease downgraded")
			}
		})
	}
}

func TestNFOCommitRecoveryLeaseTakeoverFencesOldHolder(t *testing.T) {
	f, l, _ := nfoCommitFixture(t)
	record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal("owned recovery journal unavailable")
	}
	stopNFOCommitJob(t, f, l, "failed")
	first, err := f.s.AcquireNFOWriteCommitRecovery(f.ctx, l.Job.ID, "recovery-a", time.Second)
	if err != nil {
		t.Fatal("first recovery lease unavailable")
	}
	renewed, err := f.s.RenewNFOWriteCommitRecovery(f.ctx, first, 2*time.Second)
	if err != nil || renewed.RecoveryEpoch != 1 || !renewed.ExpiresAt.After(first.ExpiresAt) {
		t.Fatal("held recovery lease not renewed")
	}
	time.Sleep(time.Until(renewed.ExpiresAt) + 200*time.Millisecond)
	if _, err := f.s.GetNFOWriteCommitFiles(f.ctx, first, 1, record.Token); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("expired recovery lease still observed evidence")
	}
	second, err := f.s.AcquireNFOWriteCommitRecovery(f.ctx, l.Job.ID, "recovery-b", time.Minute)
	if err != nil || second.RecoveryEpoch != 2 || second.Owner != "recovery-b" {
		t.Fatal("expired recovery lease not taken over")
	}
	if _, err := f.s.RenewNFOWriteCommitRecovery(f.ctx, first, time.Minute); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("replaced holder renewed")
	}
	stale := second
	stale.RecoveryEpoch = 1
	if _, err := f.s.GetNFOWriteCommitFiles(f.ctx, stale, 1, record.Token); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("stale epoch admitted")
	}
	if evidence, err := f.s.GetNFOWriteCommitFiles(f.ctx, second, 1, record.Token); err != nil || evidence.Record.Token != record.Token || evidence.Record.Owner != l.Owner {
		t.Fatal("new holder cannot observe the original token")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_commit_recovery_leases WHERE job_id=$1::uuid`, l.Job.ID); err == nil {
		t.Fatal("recovery lease deleted")
	}
}

func TestNFOCommitRecoveryLeaseRequiresStoppedJournal(t *testing.T) {
	f, l, _ := nfoCommitFixture(t)
	stopNFOCommitJob(t, f, l, "failed")
	if _, err := f.s.AcquireNFOWriteCommitRecovery(f.ctx, l.Job.ID, "recovery-a", time.Minute); err == nil {
		t.Fatal("recovery admitted without a journal")
	}
	var leases int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_recovery_leases`).Scan(&leases); err != nil || leases != 0 {
		t.Fatal("refused recovery left a lease")
	}
	if _, err := f.s.AcquireNFOWriteCommitRecovery(f.ctx, l.Job.ID, "recovery-a", 2*time.Hour); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unbounded recovery lease accepted")
	}
}

// An expired NFO write that holds a journal stops at the next claim sweep, so
// recovery is reachable without any direct state change.
func TestNFOCommitRecoveryAfterExpiredWorker(t *testing.T) {
	f, l, _ := nfoCommitFixture(t)
	record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal("owned recovery journal unavailable")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal("owned lease expiry failed")
	}
	if _, err := f.s.ClaimJob(f.ctx, "sweeper", false, time.Minute); err != nil && !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("claim sweep failed")
	}
	var state, code string
	var owner *string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT state,error_code,owner FROM jobs WHERE id=$1::uuid`, l.Job.ID).Scan(&state, &code, &owner); err != nil || state != "failed" || code != "job_timeout" || owner != nil {
		t.Fatal("expired journaled write was not stopped for recovery")
	}
	lease, err := f.s.AcquireNFOWriteCommitRecovery(f.ctx, l.Job.ID, "recovery-a", time.Minute)
	if err != nil {
		t.Fatal("recovery unreachable after expiry sweep")
	}
	if again, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1); err != nil || again.Token != record.Token {
		t.Fatal("recovery after sweep did not reopen the token")
	}
}
