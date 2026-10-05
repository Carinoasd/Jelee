package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// abruptSettleStore stops the real child process at one settlement boundary:
// right after the backup phase commits (no Rename yet), after the Rename but
// before the replaced phase commits, or right after the replaced phase commits.
type abruptSettleStore struct {
	*Store
	mode string
}

var abruptSettleExit = map[string]int{"backed-up": 91, "renamed": 92, "replaced": 93}

func (s *abruptSettleStore) SaveNFOWriteCommitSettlement(ctx context.Context, l domain.JobLease, seq int, token string, attempt uint8, phase domain.NFOWriteCommitSettlementPhase) (domain.NFOWriteCommitSettlement, error) {
	if s.mode == "renamed" && phase == domain.NFOWriteCommitReplaced {
		os.Exit(abruptSettleExit[s.mode])
	}
	saved, err := s.Store.SaveNFOWriteCommitSettlement(ctx, l, seq, token, attempt, phase)
	if err == nil && (s.mode == "backed-up" && phase == domain.NFOWriteCommitBackedUp || s.mode == "replaced" && phase == domain.NFOWriteCommitReplaced) {
		os.Exit(abruptSettleExit[s.mode])
	}
	return saved, err
}

func TestNFOWriteSettlementActualProcessRecovery(t *testing.T) {
	for _, test := range []struct{ mode, resume string }{
		{"backed-up", "recovery"}, {"renamed", "recovery"}, {"replaced", "recovery"},
		{"backed-up", "same-lease"}, {"renamed", "same-lease"},
	} {
		t.Run(test.mode+"/"+test.resume, func(t *testing.T) {
			s := newNFOWriteJob(t, 1)
			p := s.prepared[0]
			l := s.claim(t, nfoWriteTestOwner)
			ctx, cancel := context.WithTimeout(s.f.ctx, 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFOWriteSettlementActualProcessHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "JELEE_SETTLE_CHILD_DATABASE="+s.f.s.Pool.Config().ConnString(), "JELEE_SETTLE_CHILD_JOB="+l.Job.ID, "JELEE_SETTLE_CHILD_OWNER="+l.Owner, "JELEE_SETTLE_CHILD_GENERATION="+strconv.FormatInt(l.Generation, 10), "JELEE_SETTLE_CHILD_MODE="+test.mode)
			privateOutput, err := cmd.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != abruptSettleExit[test.mode] {
				if exited != nil {
					t.Log("owned child exit code", exited.ExitCode())
				}
				for _, marker := range []string{"settle child unavailable", "settle child failed before boundary", "settle child finished without abrupt exit"} {
					if bytes.Contains(privateOutput, []byte(marker)) {
						t.Log("owned child marker", marker)
					}
				}
				t.Fatal("owned child did not stop at the settlement boundary")
			}
			wantTarget := p.Original
			if test.mode != "backed-up" {
				wantTarget = p.Replacement
			}
			if !bytes.Equal(s.target(t, 0), wantTarget) {
				t.Fatal("abrupt child left an unexpected target")
			}
			fresh, err := Open(s.f.ctx, s.f.s.Pool.Config().ConnString(), 4)
			if err != nil {
				t.Fatal("fresh settlement store unavailable")
			}
			defer fresh.Pool.Close()
			if test.resume == "same-lease" {
				// The child died but the lease is still live: the same owner
				// continues through a new connection and finishes the job.
				entries, err := fresh.ListNFOWriteCommitEntries(s.f.ctx, l)
				if err != nil || len(entries) != 1 || entries[0].Settlement != domain.NFOWriteCommitBackedUp {
					t.Fatal("durable backup phase missing after abrupt child", err)
				}
				worker, err := app.NewNFOWriteWorker(fresh, nfoWriteTestWriter(t), nfoWriteTestOwner, 10*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				if err := worker.Run(s.f.ctx, l); err != nil {
					t.Fatal("same lease did not resume the settlement", err)
				}
				if resolved, err := fresh.FinishNFOWriteJob(s.f.ctx, l, domain.JobSucceeded, ""); err != nil || !resolved {
					t.Fatal("resumed job did not succeed", err)
				}
			} else {
				if _, err := fresh.Pool.Exec(s.f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := fresh.ClaimJob(s.f.ctx, "sweeper", false, time.Minute); !errors.Is(err, domain.ErrNotFound) {
					t.Fatal("claim sweep unavailable", err)
				}
				if state, code := s.state(t); state != domain.JobFailed || code != "job_timeout" {
					t.Fatal("expired journaled write not stopped", state, code)
				}
				worker, err := app.NewNFOWriteWorker(fresh, nfoWriteTestWriter(t), "b0000000-0000-4000-8000-000000000002", 10*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				state, err := worker.Recover(s.f.ctx, l.Job.ID)
				if err != nil || state != domain.JobSucceeded {
					t.Fatal("recovery lease did not finish the settlement", state, err)
				}
			}
			if !bytes.Equal(s.target(t, 0), p.Replacement) || !bytes.Equal(s.backup(t, 0), p.Original) {
				t.Fatal("resumed target or backup differs")
			}
			if state, code := s.state(t); state != domain.JobSucceeded || code != "" {
				t.Fatal("resumed job state differs", state, code)
			}
			var phases, backups int
			if err := fresh.Pool.QueryRow(s.f.ctx, `SELECT count(*),count(*) FILTER(WHERE s.phase=1) FROM nfo_write_commit_settlements s JOIN nfo_write_commit_journal w ON w.token=s.token WHERE w.job_id=$1::uuid`, l.Job.ID).Scan(&phases, &backups); err != nil || phases != 2 || backups != 1 {
				t.Fatal("resumed settlement phases differ", phases, backups, err)
			}
			s.resolved(t)
		})
	}
}

func TestNFOWriteSettlementActualProcessHelper(t *testing.T) {
	dsn := os.Getenv("JELEE_SETTLE_CHILD_DATABASE")
	if dsn == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn, 2)
	if err != nil {
		t.Fatal("settle child unavailable")
	}
	defer store.Pool.Close()
	generation, err := strconv.ParseInt(os.Getenv("JELEE_SETTLE_CHILD_GENERATION"), 10, 64)
	if err != nil {
		t.Fatal("settle child unavailable")
	}
	lease := domain.JobLease{Job: domain.Job{ID: os.Getenv("JELEE_SETTLE_CHILD_JOB"), Kind: domain.JobNFOWrite}, Owner: os.Getenv("JELEE_SETTLE_CHILD_OWNER"), Generation: generation}
	worker, err := app.NewNFOWriteWorker(&abruptSettleStore{store, os.Getenv("JELEE_SETTLE_CHILD_MODE")}, nfoWriteTestWriter(t), nfoWriteTestOwner, 10*time.Second)
	if err != nil {
		t.Fatal("settle child unavailable")
	}
	if err := worker.Run(ctx, lease); err != nil {
		t.Fatal("settle child failed before boundary")
	}
	t.Fatal("settle child finished without abrupt exit")
}
