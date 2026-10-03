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

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNFOCommitCheckpointReplayAndReadyConsistency(t *testing.T) {
	f, l, p := nfoCommitFixture(t)
	record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal("checkpoint journal unavailable")
	}
	if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, commitPlanFixture(p)); err != nil {
		t.Fatal("checkpoint plan unavailable")
	}
	ready := commitReadyFixture() // Synthetic SQL fixture, not filesystem authority.
	ready.OutputIdentity[1] = p.NativeObservation.NFOFileIdentity()[1]
	ready.RollbackIdentity[1] = ready.OutputIdentity[1]
	first := domain.NFOWriteCommitFileCheckpoint{Phase: 1, OutputIdentity: ready.OutputIdentity}
	complete := domain.NFOWriteCommitFileCheckpoint{Phase: 2, OutputIdentity: ready.OutputIdentity, RollbackIdentity: ready.RollbackIdentity}
	if _, err := f.s.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, record.Token, complete); err == nil {
		t.Fatal("complete checkpoint admitted without first output")
	}
	if saved, err := f.s.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, record.Token, first); err != nil || saved != first {
		t.Fatal("first checkpoint persistence failed")
	}
	if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, record.Token, ready); err == nil {
		t.Fatal("partial checkpoint bypassed by ready")
	}
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_files_ready(token,output_identity,rollback_identity) VALUES($1::uuid,$2,$3)`, record.Token, ready.OutputIdentity[:], ready.RollbackIdentity[:])
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "23514" || denied.Message != "nfo ready checkpoint incomplete or conflicting" {
		t.Fatal("raw partial ready rejected for unrelated reason")
	}
	if saved, err := f.s.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, record.Token, complete); err != nil || saved != complete {
		t.Fatal("complete checkpoint persistence failed")
	}
	if saved, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, record.Token, ready); err != nil || saved != ready {
		t.Fatal("complete checkpoint ready failed")
	}
	fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal("reopen checkpoint store failed")
	}
	defer fresh.Pool.Close()
	for _, value := range []domain.NFOWriteCommitFileCheckpoint{first, complete, first} {
		if saved, err := fresh.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, record.Token, value); err != nil || saved != value {
			t.Fatal("checkpoint replay changed first observations")
		}
	}
	evidence, err := fresh.GetNFOWriteCommitFiles(f.ctx, l, 1, record.Token)
	if err != nil || !evidence.CheckpointRecorded || !evidence.ReadyRecorded || evidence.Checkpoint != complete {
		t.Fatal("replay regressed complete checkpoint")
	}
	changed := first
	changed.OutputIdentity[16]++
	if _, err := fresh.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, record.Token, changed); err == nil {
		t.Fatal("changed checkpoint replay admitted")
	}
	for _, query := range []string{`DELETE FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid`, `UPDATE nfo_write_commit_file_checkpoints SET output_identity=set_byte(output_identity,16,get_byte(output_identity,16)#1) WHERE token=$1::uuid`} {
		_, err := f.s.Pool.Exec(f.ctx, query, record.Token)
		var failure *pgconn.PgError
		if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "nfo commit file evidence is immutable" {
			t.Fatal("checkpoint mutation refused for unrelated reason")
		}
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid`, record.Token).Scan(&count); err != nil || count != 2 {
		t.Fatal("checkpoint evidence unbounded or missing")
	}
}

type abruptCheckpointStore struct {
	*Store
	phase uint8
}

func (s *abruptCheckpointStore) SaveNFOWriteCommitFileCheckpoint(ctx context.Context, l domain.JobLease, seq int, token string, value domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	saved, err := s.Store.SaveNFOWriteCommitFileCheckpoint(ctx, l, seq, token, value)
	if err == nil && value.Phase == s.phase {
		os.Exit(90 + int(s.phase))
	}
	return saved, err
}

func TestNFOCommitCheckpointActualProcessResume(t *testing.T) {
	for _, phase := range []uint8{1, 2} {
		t.Run(strconv.Itoa(int(phase)), func(t *testing.T) {
			f, l, p := nfoCommitFixture(t)
			record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
			if err != nil {
				t.Fatal("owned checkpoint journal unavailable")
			}
			ctx, cancel := context.WithTimeout(f.ctx, 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFOCommitCheckpointActualProcessHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "JELEE_CHECKPOINT_CHILD_DATABASE="+f.s.Pool.Config().ConnString(), "JELEE_CHECKPOINT_CHILD_JOB="+l.Job.ID, "JELEE_CHECKPOINT_CHILD_OWNER="+l.Owner, "JELEE_CHECKPOINT_CHILD_GENERATION="+strconv.FormatInt(l.Generation, 10), "JELEE_CHECKPOINT_CHILD_TOKEN="+record.Token, "JELEE_CHECKPOINT_CHILD_PHASE="+strconv.Itoa(int(phase)))
			privateOutput, err := cmd.CombinedOutput()
			_ = privateOutput
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 90+int(phase) {
				if exited != nil {
					t.Log("owned child exit code", exited.ExitCode())
				}
				for _, marker := range []string{"owned child database unavailable", "owned child generation unavailable", "owned child phase unavailable", "owned child evidence unavailable", "owned child task unavailable", "owned child source unavailable", "owned child failed before committed checkpoint", "owned child reached ready without abrupt exit", "checkpoint child ErrChanged", "checkpoint child ErrInvalidInput", "checkpoint child ErrReplace", "checkpoint child ErrFileLock", "checkpoint child ErrConflict", "checkpoint child ErrJobLeaseLost", "checkpoint child ErrForbidden", "checkpoint child ErrDatabase"} {
					if bytes.Contains(privateOutput, []byte(marker)) {
						t.Log("owned child marker", marker)
					}
				}
				t.Fatal("owned child did not reach committed checkpoint")
			}
			before, err := f.s.GetNFOWriteCommitFiles(f.ctx, l, 1, record.Token)
			if err != nil || !before.CheckpointRecorded || before.Checkpoint.Phase != phase || before.ReadyRecorded {
				t.Fatal("abrupt child lost committed partial evidence")
			}
			var firstTime time.Time
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT recorded_at FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid AND phase=1`, record.Token).Scan(&firstTime); err != nil {
				t.Fatal("first checkpoint timestamp missing")
			}
			if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations WHERE id=$1::uuid`, p.ID); err != nil {
				t.Fatal("owned TTL cleanup failed")
			}
			fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
			if err != nil {
				t.Fatal("fresh checkpoint store unavailable")
			}
			defer fresh.Pool.Close()
			source, err := nfo.ReadSource(f.ctx, p.Scope.Source.RootPath, p.Scope.Source.RelativePath, p.Request.MaxBytes)
			if err != nil {
				t.Fatal("fresh source unavailable")
			}
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
			writer, _ := nfo.NewWriterWithBudget(budget)
			if err := writer.StageCommitFiles(f.ctx, source, l, record, fresh); err != nil {
				t.Fatal("true PG checkpoint did not resume")
			}
			after, err := fresh.GetNFOWriteCommitFiles(f.ctx, l, 1, record.Token)
			if err != nil || !after.ReadyRecorded || after.Checkpoint.Phase != 2 || after.Checkpoint.OutputIdentity != before.Checkpoint.OutputIdentity || (phase == 2 && after.Checkpoint.RollbackIdentity != before.Checkpoint.RollbackIdentity) {
				t.Fatal("resume changed first observations")
			}
			var sameTime bool
			if err := fresh.Pool.QueryRow(f.ctx, `SELECT recorded_at=$2 FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid AND phase=1`, record.Token, firstTime).Scan(&sameTime); err != nil || !sameTime {
				t.Fatal("resume changed first checkpoint timestamp")
			}
			data, err := os.ReadFile(filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath)))
			if err != nil || !bytes.Equal(data, p.Original) {
				t.Fatal("checkpoint resume changed target")
			}
		})
	}
}

func TestNFOCommitCheckpointActualProcessHelper(t *testing.T) {
	dsn := os.Getenv("JELEE_CHECKPOINT_CHILD_DATABASE")
	if dsn == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn, 2)
	if err != nil {
		t.Fatal("owned child database unavailable")
	}
	defer store.Pool.Close()
	generation, err := strconv.ParseInt(os.Getenv("JELEE_CHECKPOINT_CHILD_GENERATION"), 10, 64)
	if err != nil {
		t.Fatal("owned child generation unavailable")
	}
	phase, err := strconv.Atoi(os.Getenv("JELEE_CHECKPOINT_CHILD_PHASE"))
	if err != nil {
		t.Fatal("owned child phase unavailable")
	}
	lease := domain.JobLease{Job: domain.Job{ID: os.Getenv("JELEE_CHECKPOINT_CHILD_JOB")}, Owner: os.Getenv("JELEE_CHECKPOINT_CHILD_OWNER"), Generation: generation}
	evidence, err := store.GetNFOWriteCommitFiles(ctx, lease, 1, os.Getenv("JELEE_CHECKPOINT_CHILD_TOKEN"))
	if err != nil {
		t.Fatal("owned child evidence unavailable")
	}
	task, err := store.GetNFOWriteTask(ctx, lease, 1)
	if err != nil {
		t.Fatal("owned child task unavailable")
	}
	source, err := nfo.ReadSource(ctx, task.Preparation.Scope.Source.RootPath, task.Preparation.Scope.Source.RelativePath, task.Preparation.Request.MaxBytes)
	if err != nil {
		t.Fatal("owned child source unavailable")
	}
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	writer, _ := nfo.NewWriterWithBudget(budget)
	if err := writer.StageCommitFiles(ctx, source, lease, evidence.Record, &abruptCheckpointStore{store, uint8(phase)}); err != nil {
		for _, candidate := range []struct {
			err   error
			label string
		}{
			{nfo.ErrChanged, "checkpoint child ErrChanged"}, {nfo.ErrInvalidInput, "checkpoint child ErrInvalidInput"}, {nfo.ErrReplace, "checkpoint child ErrReplace"}, {nfo.ErrFileLock, "checkpoint child ErrFileLock"},
			{domain.ErrConflict, "checkpoint child ErrConflict"}, {domain.ErrJobLeaseLost, "checkpoint child ErrJobLeaseLost"}, {domain.ErrForbidden, "checkpoint child ErrForbidden"}, {domain.ErrDatabase, "checkpoint child ErrDatabase"},
		} {
			if errors.Is(err, candidate.err) {
				t.Log(candidate.label)
			}
		}
		t.Fatal("owned child failed before committed checkpoint")
	}
	t.Fatal("owned child reached ready without abrupt exit")
}

func TestNFOCommitCheckpointCatalogAfterFlush(t *testing.T) {
	for _, phase := range []string{"first", "replay", "complete"} {
		for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
			t.Run(phase+"/"+string(isolation), func(t *testing.T) {
				f, l, p := nfoCommitFixture(t)
				record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
				if err != nil {
					t.Fatal("catalog checkpoint journal unavailable")
				}
				if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, commitPlanFixture(p)); err != nil {
					t.Fatal("catalog checkpoint plan unavailable")
				}
				ready := commitReadyFixture()
				ready.OutputIdentity[1] = p.NativeObservation.NFOFileIdentity()[1]
				ready.RollbackIdentity[1] = ready.OutputIdentity[1]
				first := domain.NFOWriteCommitFileCheckpoint{Phase: 1, OutputIdentity: ready.OutputIdentity}
				retained := 0
				if phase != "first" {
					if _, err := f.s.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, record.Token, first); err != nil {
						t.Fatal("catalog first checkpoint unavailable")
					}
					retained = 1
				}
				tx, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
				if err != nil {
					t.Fatal("catalog test transaction unavailable")
				}
				defer tx.Rollback(f.ctx)
				switch phase {
				case "first":
					_, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_commit_file_checkpoints(token,phase,output_identity) VALUES($1::uuid,1,$2)`, record.Token, ready.OutputIdentity[:])
				case "replay":
					_, err = tx.Exec(f.ctx, `UPDATE nfo_write_commit_file_checkpoints SET token=token WHERE token=$1::uuid AND phase=1`, record.Token)
				case "complete":
					_, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_commit_file_checkpoints(token,phase,output_identity,rollback_identity) VALUES($1::uuid,2,$2,$3)`, record.Token, ready.OutputIdentity[:], ready.RollbackIdentity[:])
				}
				if err != nil {
					t.Fatal("catalog checkpoint write failed before mutation")
				}
				if _, err := tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
					t.Fatal("catalog checkpoint early flush failed")
				}
				_, err = tx.Exec(f.ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.LibraryID)
				var denied *pgconn.PgError
				if !errors.As(err, &denied) || denied.Code != "23514" || denied.Message != "nfo catalog scope changed" {
					t.Fatal("checkpoint catalog mutation after flush admitted or refused for unrelated cause")
				}
				_ = tx.Rollback(f.ctx)
				var count int
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid`, record.Token).Scan(&count); err != nil || count != retained {
					t.Fatal("checkpoint catalog rollback changed retained evidence")
				}
			})
		}
	}
}

func TestNFOCommitCheckpointFirstProofForeignKey(t *testing.T) {
	f, l, p := nfoCommitFixture(t)
	record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal("first proof journal unavailable")
	}
	if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, commitPlanFixture(p)); err != nil {
		t.Fatal("first proof plan unavailable")
	}
	ready := commitReadyFixture()
	ready.OutputIdentity[1] = p.NativeObservation.NFOFileIdentity()[1]
	ready.RollbackIdentity[1] = ready.OutputIdentity[1]
	first := domain.NFOWriteCommitFileCheckpoint{Phase: 1, OutputIdentity: ready.OutputIdentity}
	if _, err := f.s.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, record.Token, first); err != nil {
		t.Fatal("first proof unavailable")
	}
	different := ready.OutputIdentity
	different[16] += 10
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_file_checkpoints(token,phase,output_identity,rollback_identity) VALUES($1::uuid,2,$2,$3)`, record.Token, different[:], ready.RollbackIdentity[:])
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "23503" || denied.ConstraintName != "nfo_commit_checkpoint_first_output" {
		t.Fatal("complete checkpoint first output changed or unrelated refusal")
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid`, record.Token).Scan(&count); err != nil || count != 1 {
		t.Fatal("first proof refusal left partial checkpoint")
	}
}

func TestNFOCommitCheckpointMigrationRetainsHistoricalReady(t *testing.T) {
	for _, history := range []string{"plan_only", "ready"} {
		t.Run(history, func(t *testing.T) {
			f, l, p := nfoCommitFixture(t)
			jobMetricMigration(t, f, "down", 54)
			record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
			if err != nil {
				t.Fatal("historical checkpoint journal unavailable")
			}
			if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, commitPlanFixture(p)); err != nil {
				t.Fatal("historical checkpoint plan unavailable")
			}
			ready := commitReadyFixture()
			ready.OutputIdentity[1] = p.NativeObservation.NFOFileIdentity()[1]
			ready.RollbackIdentity[1] = ready.OutputIdentity[1]
			var firstTime time.Time
			if history == "ready" {
				if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, record.Token, ready); err != nil {
					t.Fatal("historical ready unavailable")
				}
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT recorded_at FROM nfo_write_commit_files_ready WHERE token=$1::uuid`, record.Token).Scan(&firstTime); err != nil {
					t.Fatal("historical ready timestamp unavailable")
				}
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',owner=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE id=$1::uuid`, l.Job.ID); err != nil {
				t.Fatal("stop historical job failed")
			}
			before := jobMetricMigrationStorage(t, f)
			jobMetricMigration(t, f, "up", SchemaVersion)
			if jobMetricMigrationStorage(t, f) != before {
				t.Fatal("checkpoint migration changed metrics")
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid`, record.Token).Scan(&count); err != nil {
				t.Fatal("migrated checkpoint query failed")
			}
			expected := 0
			if history == "ready" {
				expected = 2
			}
			if count != expected {
				t.Fatal("migration invented partial-stage proof or lost ready")
			}
			if history == "ready" {
				var matched int
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid AND output_identity=$2 AND recorded_at=$3 AND (phase=1 OR rollback_identity=$4)`, record.Token, ready.OutputIdentity[:], firstTime, ready.RollbackIdentity[:]).Scan(&matched); err != nil || matched != 2 {
					t.Fatal("migration changed first retained ready observations")
				}
			}
			nfoMigrationDenied(t, f, "000055_nfo_commit_checkpoints.down.sql")
			top := downgradeAboveMigration(t, f, "nfo_commit_recovery_leases")
			if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
				t.Fatal("retained checkpoint or journal downgraded")
			}
			version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
			if err != nil || version != top-1 || !dirty {
				t.Fatal("retained downgrade lost dirty status")
			}
			var kept int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid`, record.Token).Scan(&kept); err != nil || kept != expected {
				t.Fatal("failed downgrade removed checkpoints")
			}
		})
	}
}

func TestNFOCommitCheckpointLeaseAndActorFences(t *testing.T) {
	for _, mode := range []string{"first", "replay"} {
		for _, reason := range []string{"expiry", "cancel", "disabled", "not_admin"} {
			t.Run(mode+"/"+reason, func(t *testing.T) {
				f, l, p := nfoCommitFixture(t)
				record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
				if err != nil {
					t.Fatal("fence journal unavailable")
				}
				if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, commitPlanFixture(p)); err != nil {
					t.Fatal("fence plan unavailable")
				}
				ready := commitReadyFixture()
				ready.OutputIdentity[1] = p.NativeObservation.NFOFileIdentity()[1]
				first := domain.NFOWriteCommitFileCheckpoint{Phase: 1, OutputIdentity: ready.OutputIdentity}
				retained := 0
				if mode == "replay" {
					if _, err := f.s.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, record.Token, first); err != nil {
						t.Fatal("fence first checkpoint unavailable")
					}
					retained = 1
				}
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
					t.Fatal("owned fence mutation failed")
				}
				value, err := f.s.SaveNFOWriteCommitFileCheckpoint(f.ctx, l, 1, record.Token, first)
				if err == nil || value != (domain.NFOWriteCommitFileCheckpoint{}) {
					t.Fatal("expired/cancelled/forbidden checkpoint admitted or leaked partial return")
				}
				if mode == "first" {
					_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_commit_file_checkpoints(token,phase,output_identity) VALUES($1::uuid,1,$2)`, record.Token, first.OutputIdentity[:])
				} else {
					_, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_write_commit_file_checkpoints SET token=token WHERE token=$1::uuid AND phase=1`, record.Token)
				}
				var denied *pgconn.PgError
				expected := "nfo commit file lease is not live"
				if reason == "disabled" || reason == "not_admin" {
					expected = "nfo commit file actor is not active"
				}
				if !errors.As(err, &denied) || denied.Code != "23514" || denied.Message != expected {
					t.Fatal("raw checkpoint fence refused for unrelated reason")
				}
				var count int
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid`, record.Token).Scan(&count); err != nil || count != retained {
					t.Fatal("fenced checkpoint changed evidence")
				}
			})
		}
	}
}
