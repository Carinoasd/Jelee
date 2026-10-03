package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// abruptAttemptStore stops the real child process at one durable boundary.
// "create" modes exit before the first checkpoint commits, leaving created
// names without evidence; the others exit right after a committed record.
type abruptAttemptStore struct {
	*Store
	mode string
}

func (s *abruptAttemptStore) SaveNFOWriteCommitFileCheckpoint(ctx context.Context, l domain.JobLease, seq int, token string, value domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	if s.mode == "legacy-create" {
		os.Exit(81)
	}
	return s.Store.SaveNFOWriteCommitFileCheckpoint(ctx, l, seq, token, value)
}
func (s *abruptAttemptStore) SaveNFOWriteCommitAttemptCheckpoint(ctx context.Context, l domain.JobLease, seq int, token string, attempt uint8, value domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	if s.mode == "attempt-create" {
		os.Exit(82)
	}
	saved, err := s.Store.SaveNFOWriteCommitAttemptCheckpoint(ctx, l, seq, token, attempt, value)
	if err == nil && (s.mode == "attempt-phase1" && value.Phase == 1 || s.mode == "attempt-phase2" && value.Phase == 2) {
		os.Exit(82 + int(value.Phase))
	}
	return saved, err
}
func (s *abruptAttemptStore) SaveNFOWriteCommitAttemptReady(ctx context.Context, l domain.JobLease, seq int, token string, attempt uint8, value domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	saved, err := s.Store.SaveNFOWriteCommitAttemptReady(ctx, l, seq, token, attempt, value)
	if err == nil && s.mode == "attempt-ready" {
		os.Exit(85)
	}
	return saved, err
}

var abruptAttemptExit = map[string]int{"legacy-create": 81, "attempt-create": 82, "attempt-phase1": 83, "attempt-phase2": 84, "attempt-ready": 85}

func TestNFOCommitAttemptActualProcessRecovery(t *testing.T) {
	for _, mode := range []string{"legacy-create", "attempt-create", "attempt-phase1", "attempt-phase2", "attempt-ready"} {
		t.Run(mode, func(t *testing.T) {
			f, l, p := nfoCommitFixture(t)
			record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
			if err != nil {
				t.Fatal("owned attempt journal unavailable")
			}
			child := func(mode string) {
				ctx, cancel := context.WithTimeout(f.ctx, 25*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFOCommitAttemptActualProcessHelper$", "-test.count=1")
				cmd.Env = append(os.Environ(), "JELEE_ATTEMPT_CHILD_DATABASE="+f.s.Pool.Config().ConnString(), "JELEE_ATTEMPT_CHILD_JOB="+l.Job.ID, "JELEE_ATTEMPT_CHILD_OWNER="+l.Owner, "JELEE_ATTEMPT_CHILD_GENERATION="+strconv.FormatInt(l.Generation, 10), "JELEE_ATTEMPT_CHILD_TOKEN="+record.Token, "JELEE_ATTEMPT_CHILD_MODE="+mode)
				privateOutput, err := cmd.CombinedOutput()
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != abruptAttemptExit[mode] {
					if exited != nil {
						t.Log("owned child exit code", exited.ExitCode())
					}
					for _, marker := range []string{"attempt child unavailable", "attempt child failed before boundary", "attempt child finished without abrupt exit", "attempt child ErrChanged", "attempt child ErrReplace", "attempt child ErrConflict", "attempt child ErrDatabase", "attempt child ErrJobLeaseLost", "attempt child exhausted"} {
						if bytes.Contains(privateOutput, []byte(marker)) {
							t.Log("owned child marker", marker)
						}
					}
					t.Fatal("owned child did not stop at the durable boundary")
				}
			}
			child("legacy-create")
			if mode != "legacy-create" {
				child(mode)
			}
			before, err := f.s.GetNFOWriteCommitAttempts(f.ctx, l, 1, record.Token)
			if err != nil {
				t.Fatal("owned attempt evidence unavailable")
			}
			var firstTime time.Time
			if mode == "attempt-phase1" || mode == "attempt-phase2" || mode == "attempt-ready" {
				if !before.Attempts[0].CheckpointRecorded {
					t.Fatal("abrupt child lost committed attempt checkpoint")
				}
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT recorded_at FROM nfo_write_commit_attempt_checkpoints WHERE token=$1::uuid AND attempt=1 AND phase=1`, record.Token).Scan(&firstTime); err != nil {
					t.Fatal("first attempt checkpoint timestamp missing")
				}
			}
			if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations WHERE id=$1::uuid`, p.ID); err != nil {
				t.Fatal("owned TTL cleanup failed")
			}
			fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
			if err != nil {
				t.Fatal("fresh attempt store unavailable")
			}
			defer fresh.Pool.Close()
			source, err := nfo.ReadSource(f.ctx, p.Scope.Source.RootPath, p.Scope.Source.RelativePath, p.Request.MaxBytes)
			if err != nil {
				t.Fatal("fresh source unavailable")
			}
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
			writer, _ := nfo.NewWriterWithBudget(budget)
			if err := writer.StageCommitFiles(f.ctx, source, l, record, fresh); err != nil {
				t.Fatal("true PG attempt did not recover after abrupt child")
			}
			after, err := fresh.GetNFOWriteCommitAttempts(f.ctx, l, 1, record.Token)
			if err != nil {
				t.Fatal("recovered attempt evidence unavailable")
			}
			selected, allocated := uint8(1), 1
			if mode == "attempt-create" {
				selected, allocated = 2, 2
			}
			for i, entry := range after.Attempts {
				if (i < allocated) != (entry.Number != 0) || entry.ReadyRecorded != (entry.Number == selected) {
					t.Fatal("recovery allocated or selected the wrong attempt")
				}
			}
			chosen := after.Attempts[selected-1]
			if chosen.Checkpoint.Phase != 2 || chosen.Ready.OutputIdentity != chosen.Checkpoint.OutputIdentity || chosen.Ready.RollbackIdentity != chosen.Checkpoint.RollbackIdentity {
				t.Fatal("selected attempt evidence incomplete")
			}
			if !firstTime.IsZero() {
				if chosen.Checkpoint.OutputIdentity != before.Attempts[0].Checkpoint.OutputIdentity || (mode != "attempt-phase1" && chosen.Checkpoint.RollbackIdentity != before.Attempts[0].Checkpoint.RollbackIdentity) {
					t.Fatal("recovery changed first attempt observations")
				}
				var same bool
				if err := fresh.Pool.QueryRow(f.ctx, `SELECT recorded_at=$2 FROM nfo_write_commit_attempt_checkpoints WHERE token=$1::uuid AND attempt=1 AND phase=1`, record.Token, firstTime).Scan(&same); err != nil || !same {
					t.Fatal("recovery changed first attempt checkpoint timestamp")
				}
			}
			directory := filepath.Dir(filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath)))
			base := ".jelee-nfo-commit-" + strings.ReplaceAll(record.Token, "-", "")
			retained := []string{base + "-output"}
			for attempt := 1; attempt <= allocated; attempt++ {
				retained = append(retained, base+"-attempt-"+strconv.Itoa(attempt)+"-output")
			}
			for _, name := range retained {
				if _, err := os.Lstat(filepath.Join(directory, name)); err != nil {
					t.Fatal("unknown or selected namespace object missing")
				}
			}
			legacy, err := fresh.GetNFOWriteCommitFiles(f.ctx, l, 1, record.Token)
			if err != nil || !legacy.PlanRecorded || legacy.CheckpointRecorded || legacy.ReadyRecorded {
				t.Fatal("legacy evidence written beside an allocated attempt")
			}
			data, err := os.ReadFile(filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath)))
			if err != nil || !bytes.Equal(data, p.Original) {
				t.Fatal("attempt recovery changed target")
			}
		})
	}
}

func TestNFOCommitAttemptActualProcessHelper(t *testing.T) {
	dsn := os.Getenv("JELEE_ATTEMPT_CHILD_DATABASE")
	if dsn == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn, 2)
	if err != nil {
		t.Fatal("attempt child unavailable")
	}
	defer store.Pool.Close()
	generation, err := strconv.ParseInt(os.Getenv("JELEE_ATTEMPT_CHILD_GENERATION"), 10, 64)
	if err != nil {
		t.Fatal("attempt child unavailable")
	}
	lease := domain.JobLease{Job: domain.Job{ID: os.Getenv("JELEE_ATTEMPT_CHILD_JOB")}, Owner: os.Getenv("JELEE_ATTEMPT_CHILD_OWNER"), Generation: generation}
	evidence, err := store.GetNFOWriteCommitFiles(ctx, lease, 1, os.Getenv("JELEE_ATTEMPT_CHILD_TOKEN"))
	if err != nil {
		t.Fatal("attempt child unavailable")
	}
	task, err := store.GetNFOWriteTask(ctx, lease, 1)
	if err != nil {
		t.Fatal("attempt child unavailable")
	}
	source, err := nfo.ReadSource(ctx, task.Preparation.Scope.Source.RootPath, task.Preparation.Scope.Source.RelativePath, task.Preparation.Request.MaxBytes)
	if err != nil {
		t.Fatal("attempt child unavailable")
	}
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	writer, _ := nfo.NewWriterWithBudget(budget)
	if err := writer.StageCommitFiles(ctx, source, lease, evidence.Record, &abruptAttemptStore{store, os.Getenv("JELEE_ATTEMPT_CHILD_MODE")}); err != nil {
		for _, candidate := range []struct {
			err   error
			label string
		}{
			{nfo.ErrChanged, "attempt child ErrChanged"}, {nfo.ErrReplace, "attempt child ErrReplace"}, {domain.ErrConflict, "attempt child ErrConflict"},
			{domain.ErrDatabase, "attempt child ErrDatabase"}, {domain.ErrJobLeaseLost, "attempt child ErrJobLeaseLost"}, {nfo.ErrCommitAttemptsExhausted, "attempt child exhausted"},
		} {
			if errors.Is(err, candidate.err) {
				t.Log(candidate.label)
			}
		}
		t.Fatal("attempt child failed before boundary")
	}
	t.Fatal("attempt child finished without abrupt exit")
}
