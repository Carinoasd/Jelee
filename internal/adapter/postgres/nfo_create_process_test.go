package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// abruptCreateStore stops the real child process right before or after one
// durable step of a create. The filesystem steps between two saves (output
// create, write, fsync, pin, directory sync, the link) are interrupted by the
// package nfo process tests, which can reach them.
type abruptCreateStore struct {
	*Store
	mode string
}

var abruptCreateExit = map[string]int{"plan": 101, "before-checkpoint": 102, "checkpoint": 103, "ready": 104, "phase1": 105, "linked": 106, "phase2": 107}

func (s *abruptCreateStore) exit(mode string) {
	if s.mode == mode {
		os.Exit(abruptCreateExit[mode])
	}
}

func (s *abruptCreateStore) SaveNFOWriteCommitFilePlan(ctx context.Context, l domain.JobLease, seq int, token string, plan domain.NFOWriteCommitFilePlan) (domain.NFOWriteCommitFilePlan, error) {
	saved, err := s.Store.SaveNFOWriteCommitFilePlan(ctx, l, seq, token, plan)
	if err == nil {
		s.exit("plan")
	}
	return saved, err
}

func (s *abruptCreateStore) SaveNFOWriteCommitFileCheckpoint(ctx context.Context, l domain.JobLease, seq int, token string, value domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	s.exit("before-checkpoint")
	saved, err := s.Store.SaveNFOWriteCommitFileCheckpoint(ctx, l, seq, token, value)
	if err == nil {
		s.exit("checkpoint")
	}
	return saved, err
}

func (s *abruptCreateStore) SaveNFOWriteCommitFilesReady(ctx context.Context, l domain.JobLease, seq int, token string, ready domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	saved, err := s.Store.SaveNFOWriteCommitFilesReady(ctx, l, seq, token, ready)
	if err == nil {
		s.exit("ready")
	}
	return saved, err
}

func (s *abruptCreateStore) SaveNFOWriteCommitSettlement(ctx context.Context, l domain.JobLease, seq int, token string, attempt uint8, phase domain.NFOWriteCommitSettlementPhase) (domain.NFOWriteCommitSettlement, error) {
	if phase == domain.NFOWriteCommitReplaced {
		s.exit("linked")
	}
	saved, err := s.Store.SaveNFOWriteCommitSettlement(ctx, l, seq, token, attempt, phase)
	if err == nil && phase == domain.NFOWriteCommitBackedUp {
		s.exit("phase1")
	}
	if err == nil && phase == domain.NFOWriteCommitReplaced {
		s.exit("phase2")
	}
	return saved, err
}

func TestNFOCreateActualProcessRecovery(t *testing.T) {
	type outcome struct {
		mode, resume, interference string
	}
	var cases []outcome
	for _, mode := range []string{"plan", "before-checkpoint", "checkpoint", "ready", "phase1", "linked", "phase2"} {
		cases = append(cases, outcome{mode, "recovery", "none"}, outcome{mode, "recovery", "foreign"})
	}
	cases = append(cases, outcome{"ready", "same-lease", "none"}, outcome{"phase1", "same-lease", "none"}, outcome{"linked", "same-lease", "none"})
	for _, c := range cases {
		t.Run(c.mode+"/"+c.resume+"/"+c.interference, func(t *testing.T) {
			s := newNFOCreateJob(t)
			p := s.prepared[0]
			l := s.claim(t, nfoWriteTestOwner)
			ctx, cancel := context.WithTimeout(s.f.ctx, 40*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFOCreateActualProcessHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "JELEE_CREATE_CHILD_DATABASE="+s.f.s.Pool.Config().ConnString(), "JELEE_CREATE_CHILD_JOB="+l.Job.ID, "JELEE_CREATE_CHILD_OWNER="+l.Owner, "JELEE_CREATE_CHILD_GENERATION="+strconv.FormatInt(l.Generation, 10), "JELEE_CREATE_CHILD_MODE="+c.mode)
			privateOutput, err := cmd.CombinedOutput()
			_ = privateOutput // Never print private child diagnostics or payloads.
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != abruptCreateExit[c.mode] {
				t.Fatal("owned child did not stop at the create boundary", c.mode)
			}
			linked := c.mode == "linked" || c.mode == "phase2"
			if _, err := os.Lstat(s.targetPath(0)); linked == errors.Is(err, os.ErrNotExist) {
				t.Fatal("abrupt child left an unexpected target", c.mode)
			}
			// Invariant 2: a target that exists was linked after a durable phase 1;
			// without phase 1 the dead child never linked.
			recorded := s.phases(t)
			hasPhaseOne := len(recorded) > 0 && recorded[0] == 1
			if linked && !hasPhaseOne || !linked && hasPhaseOne && c.mode != "phase1" {
				t.Fatal("phase 1 and the link disagree", c.mode, recorded)
			}
			var foreign []byte
			if c.interference == "foreign" {
				foreign = []byte("<movie><title>written while the worker was dead</title></movie>")
				staged := s.targetPath(0) + ".user"
				if err := os.WriteFile(staged, foreign, 0o600); err != nil {
					t.Fatal(err)
				}
				// The name is occupied or replaced, as any user could.
				if err := os.Rename(staged, s.targetPath(0)); err != nil {
					t.Fatal(err)
				}
			}
			fresh, err := Open(s.f.ctx, s.f.s.Pool.Config().ConnString(), 6)
			if err != nil {
				t.Fatal("fresh create store unavailable")
			}
			defer fresh.Pool.Close()
			finalState := domain.JobSucceeded
			if c.resume == "same-lease" {
				worker, err := app.NewNFOWriteWorker(fresh, nfoWriteTestWriter(t), nfoWriteTestOwner, 10*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				if err := worker.Run(s.f.ctx, l); err != nil {
					t.Fatal("same lease did not resume the create", err)
				}
				if resolved, err := fresh.FinishNFOWriteJob(s.f.ctx, l, domain.JobSucceeded, ""); err != nil || !resolved {
					t.Fatal("resumed create did not succeed", err)
				}
			} else {
				if _, err := fresh.Pool.Exec(s.f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := fresh.ClaimJob(s.f.ctx, "sweeper", false, time.Minute); !errors.Is(err, domain.ErrNotFound) {
					t.Fatal("claim sweep unavailable", err)
				}
				if state, code := s.state(t); state != domain.JobFailed || code != "job_timeout" {
					t.Fatal("expired journaled create not stopped", state, code)
				}
				worker, err := app.NewNFOWriteWorker(fresh, nfoWriteTestWriter(t), "b0000000-0000-4000-8000-000000000002", 10*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				state, err := worker.Recover(s.f.ctx, l.Job.ID)
				if err != nil {
					t.Fatal("recovery lease did not conclude the create", err)
				}
				finalState = state
			}
			s.resolved(t)
			got := s.target(t, 0)
			phases := s.phases(t)
			if foreign != nil {
				// Whatever the crash left, an object that took the name is untouched.
				if !bytes.Equal(got, foreign) {
					t.Fatal("recovery touched the foreign object")
				}
				if c.mode == "phase2" {
					if finalState != domain.JobSucceeded || len(phases) != 2 {
						t.Fatal("a completed create changed state", finalState, phases)
					}
				} else if finalState != domain.JobFailed {
					t.Fatal("a create over a foreign object cannot succeed", finalState)
				}
				if c.mode == "phase1" || c.mode == "linked" {
					if len(phases) != 2 || phases[1] != 3 {
						t.Fatal("phase 3 must conclude a token that cannot link", phases)
					}
				}
				return
			}
			// Created exactly once with the prepared bytes, whatever the cut.
			if finalState != domain.JobSucceeded || !bytes.Equal(got, p.Replacement) {
				t.Fatal("the create did not complete exactly", finalState)
			}
			if len(phases) != 2 || phases[0] != 1 || phases[1] != 2 {
				t.Fatal("create phases differ", phases)
			}
			if _, err := os.Lstat(s.targetPath(0) + ".jelee.bak"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a create left a backup")
			}
			// The cleaner then removes the token's recorded names and nothing else.
			startNFOWriteRunner(t, fresh, nfoWriteTestWriter(t))
			dir := filepath.Dir(s.targetPath(0))
			deadline := time.Now().Add(30 * time.Second)
			for {
				var cleaned int
				if err := fresh.Pool.QueryRow(s.f.ctx, `SELECT count(*) FROM nfo_write_commit_cleanups c JOIN nfo_write_commit_journal w ON w.token=c.token WHERE w.job_id=$1::uuid`, l.Job.ID).Scan(&cleaned); err != nil {
					t.Fatal(err)
				}
				if cleaned == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the cleaner did not conclude the token")
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !bytes.Equal(s.target(t, 0), p.Replacement) {
				t.Fatal("cleanup changed the created target")
			}
			left := tokenNames(t, dir)
			if c.mode == "before-checkpoint" {
				// The legacy output and pin were created but never recorded: they
				// may be anything, so the cleaner keeps and counts them.
				if len(left) != 2 {
					t.Fatal("unrecorded names must be retained", left)
				}
			} else if len(left) != 0 {
				t.Fatal("recorded names were not cleaned", left)
			}
		})
	}
}

func TestNFOCreateActualProcessHelper(t *testing.T) {
	dsn := os.Getenv("JELEE_CREATE_CHILD_DATABASE")
	if dsn == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn, 2)
	if err != nil {
		t.Fatal("create child unavailable")
	}
	defer store.Pool.Close()
	generation, err := strconv.ParseInt(os.Getenv("JELEE_CREATE_CHILD_GENERATION"), 10, 64)
	if err != nil {
		t.Fatal("create child unavailable")
	}
	lease := domain.JobLease{Job: domain.Job{ID: os.Getenv("JELEE_CREATE_CHILD_JOB"), Kind: domain.JobNFOWrite}, Owner: os.Getenv("JELEE_CREATE_CHILD_OWNER"), Generation: generation}
	worker, err := app.NewNFOWriteWorker(&abruptCreateStore{store, os.Getenv("JELEE_CREATE_CHILD_MODE")}, nfoWriteTestWriter(t), nfoWriteTestOwner, 10*time.Second)
	if err != nil {
		t.Fatal("create child unavailable")
	}
	if err := worker.Run(ctx, lease); err != nil {
		t.Fatal("create child failed before its boundary")
	}
	t.Fatal("create child finished without an abrupt exit")
}

// flakyCreateStore fails one durable step of a create once, as a lost
// connection would, so the same lease has to continue from what is saved.
type flakyCreateStore struct {
	*Store
	failures map[string]int
}

func (s *flakyCreateStore) fail(step string) bool {
	if s.failures[step] > 0 {
		s.failures[step]--
		return true
	}
	return false
}

func (s *flakyCreateStore) SaveNFOWriteCommitFileCheckpoint(ctx context.Context, l domain.JobLease, seq int, token string, value domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	if s.fail("legacy-checkpoint") {
		return domain.NFOWriteCommitFileCheckpoint{}, domain.ErrDatabase
	}
	return s.Store.SaveNFOWriteCommitFileCheckpoint(ctx, l, seq, token, value)
}

func (s *flakyCreateStore) SaveNFOWriteCommitAttemptReady(ctx context.Context, l domain.JobLease, seq int, token string, attempt uint8, value domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	if s.fail("attempt-ready") {
		return domain.NFOWriteCommitFilesReady{}, domain.ErrDatabase
	}
	return s.Store.SaveNFOWriteCommitAttemptReady(ctx, l, seq, token, attempt, value)
}

func (s *flakyCreateStore) SaveNFOWriteCommitSettlement(ctx context.Context, l domain.JobLease, seq int, token string, attempt uint8, phase domain.NFOWriteCommitSettlementPhase) (domain.NFOWriteCommitSettlement, error) {
	if phase == domain.NFOWriteCommitBackedUp && s.fail("phase1") {
		return domain.NFOWriteCommitSettlement{}, domain.ErrDatabase
	}
	return s.Store.SaveNFOWriteCommitSettlement(ctx, l, seq, token, attempt, phase)
}

// A create continues in the namespace its checkpoint names, allocates the next
// one when names exist without a checkpoint (adopting nothing), and resumes a
// ready namespace before settling: each stop is a lost response, not a crash.
func TestNFOCreateContinuesAcrossAttemptNamespaces(t *testing.T) {
	s := newNFOCreateJob(t)
	p := s.prepared[0]
	l := s.claim(t, nfoWriteTestOwner)
	store := &flakyCreateStore{Store: s.f.s, failures: map[string]int{"legacy-checkpoint": 1, "attempt-ready": 1, "phase1": 1}}
	dir := filepath.Dir(s.targetPath(0))
	want := []struct {
		step  string
		names int
	}{
		{"legacy-checkpoint", 2}, // output and pin exist without a checkpoint
		{"attempt-ready", 4},     // the next namespace holds its own output and pin
		{"phase1", 4},            // ready is saved; the target is still absent
	}
	for _, step := range want {
		if err := s.runWorker(t, store, l); err == nil || errors.Is(err, app.ErrNFOWritePartial) {
			t.Fatal("the injected failure did not stop the run", step.step, err)
		}
		if _, err := os.Lstat(s.targetPath(0)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the target exists before phase 1", step.step)
		}
		if names := tokenNames(t, dir); len(names) != step.names {
			t.Fatal("namespace names differ after", step.step, names)
		}
	}
	if err := s.runWorker(t, store, l); err != nil {
		t.Fatal("the continued create failed", err)
	}
	if !bytes.Equal(s.target(t, 0), p.Replacement) {
		t.Fatal("the created file differs")
	}
	if _, err := s.f.s.FinishNFOWriteJob(s.f.ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	var attempts, ready int
	if err := s.f.s.Pool.QueryRow(s.f.ctx, `SELECT (SELECT count(*) FROM nfo_write_commit_attempts a JOIN nfo_write_commit_journal w ON w.token=a.token WHERE w.job_id=$1::uuid AND a.attempt>0),(SELECT count(*) FROM nfo_write_commit_attempt_ready r JOIN nfo_write_commit_journal w ON w.token=r.token WHERE w.job_id=$1::uuid AND r.rollback_identity IS NULL AND r.complete_phase=1)`, s.job.ID).Scan(&attempts, &ready); err != nil || attempts != 1 || ready != 1 {
		t.Fatal("the create did not settle in the next namespace", attempts, ready, err)
	}
	if got := s.phases(t); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatal("create phases differ", got)
	}
	s.resolved(t)
}
