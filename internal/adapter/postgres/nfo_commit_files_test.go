package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/jackc/pgx/v5/pgconn"
)

func commitIdentityFixture(kind, id byte) [48]byte {
	var result [48]byte
	result[0], result[1], result[2], result[16] = 1, 2, kind, id
	return result
}

func commitPlanFixture(prepared domain.NFOWritePreparation) domain.NFOWriteCommitFilePlan {
	return domain.NFOWriteCommitFilePlan{Version: 1, TargetName: filepath.Base(filepath.FromSlash(prepared.Scope.Source.RelativePath)), ParentIdentity: commitIdentityFixture(2, 1), TargetIdentity: commitIdentityFixture(1, 2)}
}

func commitReadyFixture() domain.NFOWriteCommitFilesReady {
	return domain.NFOWriteCommitFilesReady{OutputIdentity: commitIdentityFixture(1, 3), RollbackIdentity: commitIdentityFixture(1, 4)}
}

func TestNFOCommitFilePersistenceReplayAndReopen(t *testing.T) {
	f, l, prepared := nfoCommitFixture(t)
	record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal(err)
	}
	plan, ready := commitPlanFixture(prepared), commitReadyFixture()
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() {
			saved, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, plan)
			if err == nil && saved != plan {
				err = errors.New("plan replay changed")
			}
			if err == nil {
				savedReady, saveErr := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, record.Token, ready)
				err = saveErr
				if err == nil && savedReady != ready {
					err = errors.New("ready replay changed")
				}
			}
			results <- err
		}()
	}
	for i := 0; i < 16; i++ {
		if err := <-results; err != nil {
			t.Fatal("concurrent evidence replay", err)
		}
	}
	fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Pool.Close()
	if saved, err := fresh.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, plan); err != nil || saved != plan {
		t.Fatal("plan reopen", err)
	}
	if saved, err := fresh.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, record.Token, ready); err != nil || saved != ready {
		t.Fatal("ready reopen", err)
	}
	changed := plan
	changed.TargetIdentity[16]++
	if saved, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, changed); !errors.Is(err, domain.ErrConflict) || saved != (domain.NFOWriteCommitFilePlan{}) {
		t.Fatal("conflicting plan replaced evidence", err)
	}
	changedReady := ready
	changedReady.OutputIdentity[16] += 5
	if saved, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, record.Token, changedReady); !errors.Is(err, domain.ErrConflict) || saved != (domain.NFOWriteCommitFilesReady{}) {
		t.Fatal("conflicting ready replaced evidence", err)
	}
	for _, private := range []any{record, plan, ready} {
		encoded, _ := json.Marshal(private)
		if string(encoded) != "{}" || strings.Contains(fmt.Sprintf("%+v %#v", private, private), record.Token) {
			t.Fatal("private file evidence exposed")
		}
	}
	var plans, readies int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_commit_file_plans),(SELECT count(*) FROM nfo_write_commit_files_ready)`).Scan(&plans, &readies); err != nil || plans != 1 || readies != 1 {
		t.Fatal("unbounded replay evidence", err)
	}
	// These identities are synthetic storage fixtures, not physical authorization.
}

func TestNFOCommitFilePersistenceNativeAndChildReopen(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("native file evidence unsupported on this platform")
	}
	f, l, prepared := nfoCommitFixture(t)
	record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal(err)
	}
	source, err := nfo.ReadSource(f.ctx, prepared.Scope.Source.RootPath, prepared.Scope.Source.RelativePath, prepared.Request.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	writer, _ := nfo.NewWriterWithBudget(budget)
	if err := writer.StageCommitFiles(f.ctx, source, l, record, f.s); err != nil {
		t.Fatal("native files and real database persistence", err)
	}
	data, err := os.ReadFile(filepath.Join(prepared.Scope.Source.RootPath, filepath.FromSlash(prepared.Scope.Source.RelativePath)))
	if err != nil || !bytes.Equal(data, prepared.Original) {
		t.Fatal("staging replaced target")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_preparations`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFOCommitFilePersistenceChildHelper$")
	cmd.Env = append(os.Environ(), "JELEE_NFO_COMMIT_DATABASE_CHILD="+f.s.Pool.Config().ConnString(), "JELEE_NFO_COMMIT_TOKEN_CHILD="+record.Token, "JELEE_NFO_COMMIT_JOB_CHILD="+l.Job.ID, "JELEE_NFO_COMMIT_OWNER_CHILD="+l.Owner, "JELEE_NFO_COMMIT_GENERATION_CHILD="+strconv.FormatInt(l.Generation, 10))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("database and retained physical evidence reopen: %v %s", err, out)
	}
}

func TestNFOCommitFilePersistenceChildHelper(t *testing.T) {
	dsn := os.Getenv("JELEE_NFO_COMMIT_DATABASE_CHILD")
	if dsn == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn, 2)
	if err != nil {
		t.Fatal("reopen owned file evidence database")
	}
	defer store.Pool.Close()
	generation, err := strconv.ParseInt(os.Getenv("JELEE_NFO_COMMIT_GENERATION_CHILD"), 10, 64)
	if err != nil {
		t.Fatal("child lease unavailable")
	}
	lease := domain.JobLease{Job: domain.Job{ID: os.Getenv("JELEE_NFO_COMMIT_JOB_CHILD")}, Owner: os.Getenv("JELEE_NFO_COMMIT_OWNER_CHILD"), Generation: generation}
	evidence, err := store.GetNFOWriteCommitFiles(ctx, lease, 1, os.Getenv("JELEE_NFO_COMMIT_TOKEN_CHILD"))
	if err != nil || !evidence.PlanRecorded || !evidence.ReadyRecorded {
		t.Fatal("fenced evidence read unavailable", err)
	}
	task, err := store.GetNFOWriteTask(ctx, lease, 1)
	if err != nil {
		t.Fatal("owned child task unavailable", err)
	}
	source, err := nfo.ReadSource(ctx, task.Preparation.Scope.Source.RootPath, task.Preparation.Scope.Source.RelativePath, task.Preparation.Request.MaxBytes)
	if err != nil {
		t.Fatal("child source unavailable", err)
	}
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	writer, _ := nfo.NewWriterWithBudget(budget)
	if err := writer.StageCommitFiles(ctx, source, lease, evidence.Record, store); err != nil {
		t.Fatal("child ready resume rejected", err)
	}
	var plan domain.NFOWriteCommitFilePlan
	var ready domain.NFOWriteCommitFilesReady
	var parent, target, output, rollback, original, replacement []byte
	var root, relative string
	err = store.Pool.QueryRow(ctx, `SELECT p.version,p.target_name,p.parent_identity,p.target_identity,r.output_identity,r.rollback_identity,e.original_bytes,e.replacement_bytes,e.root_path,e.relative_path FROM nfo_write_commit_file_plans p JOIN nfo_write_commit_files_ready r USING(token) JOIN nfo_write_commit_journal j USING(token) JOIN nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE p.token=$1::uuid`, os.Getenv("JELEE_NFO_COMMIT_TOKEN_CHILD")).Scan(&plan.Version, &plan.TargetName, &parent, &target, &output, &rollback, &original, &replacement, &root, &relative)
	if err != nil || len(parent) != 48 || len(target) != 48 || len(output) != 48 || len(rollback) != 48 {
		t.Fatal("persisted evidence missing or malformed")
	}
	copy(plan.ParentIdentity[:], parent)
	copy(plan.TargetIdentity[:], target)
	copy(ready.OutputIdentity[:], output)
	copy(ready.RollbackIdentity[:], rollback)
	directory, err := os.OpenRoot(filepath.Join(root, filepath.Dir(filepath.FromSlash(relative))))
	if err != nil {
		t.Fatal("open held owned parent")
	}
	defer directory.Close()
	if err := nfo.VerifyCommitFiles(ctx, directory, os.Getenv("JELEE_NFO_COMMIT_TOKEN_CHILD"), plan, ready, original, replacement); err != nil {
		t.Fatal("database/native evidence differs", err)
	}
}

func TestNFOCommitFilePersistenceLeaseAndRetention(t *testing.T) {
	for _, reason := range []string{"owner", "generation", "expiry", "cancel", "disabled", "not_admin", "token", "missing_plan", "target_name", "identity"} {
		t.Run(reason, func(t *testing.T) {
			f, l, prepared := nfoCommitFixture(t)
			record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
			if err != nil {
				t.Fatal(err)
			}
			plan := commitPlanFixture(prepared)
			token := record.Token
			switch reason {
			case "owner":
				l.Owner = "other"
			case "generation":
				l.Generation++
			case "expiry":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID)
			case "cancel":
				_, err = f.s.CancelJob(f.ctx, f.a, l.Job.ID)
			case "disabled":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID)
			case "not_admin":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, f.a.UserID)
			case "token":
				token = "11111111-1111-4111-8111-111111111111"
			case "target_name":
				plan.TargetName = "other.nfo"
			case "identity":
				plan.TargetIdentity[0] = 9
			}
			if err != nil {
				t.Fatal(err)
			}
			if reason == "missing_plan" {
				if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, token, commitReadyFixture()); err == nil {
					t.Fatal("ready without plan accepted")
				}
			} else {
				if saved, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, token, plan); err == nil || saved != (domain.NFOWriteCommitFilePlan{}) {
					t.Fatal("denied plan returned evidence")
				}
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_commit_file_plans)+(SELECT count(*) FROM nfo_write_commit_files_ready)`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed evidence retained partial data", err)
			}
		})
	}
}

func TestNFOCommitFilePersistenceImmutableAndMigration(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newJobFixture(t)
		before := jobMetricMigrationStorage(t, f)
		jobMetricMigration(t, f, "down", 49)
		jobMetricMigration(t, f, "up", SchemaVersion)
		if f.s.Ready(f.ctx) != nil || jobMetricMigrationStorage(t, f) != before {
			t.Fatal("file evidence empty migration changed readiness or epoch")
		}
	})
	t.Run("retained", func(t *testing.T) {
		f, l, prepared := nfoCommitFixture(t)
		record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, commitPlanFixture(prepared)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, record.Token, commitReadyFixture()); err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{`DELETE FROM nfo_write_commit_file_plans`, `DELETE FROM nfo_write_commit_files_ready`, `UPDATE nfo_write_commit_file_plans SET target_name='other.nfo'`, `UPDATE nfo_write_commit_files_ready SET output_identity=rollback_identity`} {
			if _, err := f.s.Pool.Exec(f.ctx, query); err == nil {
				t.Fatal("file evidence mutation accepted")
			}
		}
		if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
			t.Fatal("retained file evidence downgraded")
		}
		version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
		if err != nil || version != SchemaVersion-1 || !dirty || f.s.Ready(f.ctx) == nil {
			t.Fatal("retained file downgrade lost dirty/readiness rejection", err)
		}
		var count int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_files_ready`).Scan(&count); err != nil || count != 1 {
			t.Fatal("downgrade discarded evidence", err)
		}
	})
}

func TestNFOCommitFilePersistenceDeferredLeaseAndRollback(t *testing.T) {
	for _, mode := range []string{"plan_insert", "plan_replay", "ready_insert", "ready_replay"} {
		for _, reason := range []string{"rollback", "expiry", "disabled", "cancel"} {
			t.Run(mode+"/"+reason, func(t *testing.T) {
				f, lease, prepared := nfoCommitFixture(t)
				record, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1)
				if err != nil {
					t.Fatal(err)
				}
				plan, ready := commitPlanFixture(prepared), commitReadyFixture()
				wantPlans, wantReady := 0, 0
				if mode != "plan_insert" {
					if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, 1, record.Token, plan); err != nil {
						t.Fatal(err)
					}
					wantPlans = 1
				}
				if mode == "ready_replay" {
					if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, lease, 1, record.Token, ready); err != nil {
						t.Fatal(err)
					}
					wantReady = 1
				}
				tx, err := f.s.Pool.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(f.ctx)
				switch mode {
				case "plan_insert":
					_, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_commit_file_plans(token,version,target_name,parent_identity,target_identity) VALUES($1::uuid,1,$2,$3,$4)`, record.Token, plan.TargetName, plan.ParentIdentity[:], plan.TargetIdentity[:])
				case "plan_replay":
					_, err = tx.Exec(f.ctx, `UPDATE nfo_write_commit_file_plans SET token=token WHERE token=$1::uuid`, record.Token)
				case "ready_insert":
					_, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_commit_files_ready(token,output_identity,rollback_identity) VALUES($1::uuid,$2,$3)`, record.Token, ready.OutputIdentity[:], ready.RollbackIdentity[:])
				case "ready_replay":
					_, err = tx.Exec(f.ctx, `UPDATE nfo_write_commit_files_ready SET token=token WHERE token=$1::uuid`, record.Token)
				}
				if err != nil {
					t.Fatal(err)
				}
				if reason == "rollback" {
					err = tx.Rollback(f.ctx)
				} else {
					switch reason {
					case "expiry":
						_, err = tx.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '20 milliseconds' WHERE id=$1::uuid`, lease.Job.ID)
						if err == nil {
							_, err = tx.Exec(f.ctx, `SELECT pg_sleep(0.05)`)
						}
					case "disabled":
						_, err = tx.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID)
					case "cancel":
						_, err = tx.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, lease.Job.ID)
					}
					if err != nil {
						t.Fatal(err)
					}
					err = tx.Commit(f.ctx)
					var pgErr *pgconn.PgError
					if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "nfo commit file lease is not live" {
						t.Fatal("deferred evidence lease accepted or unrelated refusal", err)
					}
				}
				var plans, readies int
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_commit_file_plans),(SELECT count(*) FROM nfo_write_commit_files_ready)`).Scan(&plans, &readies); err != nil || plans != wantPlans || readies != wantReady {
					t.Fatal("deferred refusal retained new evidence or lost earlier records", err)
				}
			})
		}
	}
}

func TestNFOCommitFilePersistenceStorageCodec(t *testing.T) {
	f := newJobFixture(t)
	for _, platform := range []byte{1, 2} {
		base := commitIdentityFixture(1, 8)
		base[1] = platform
		for index := -1; index < len(base); index++ {
			changed := base
			if index >= 0 {
				changed[index] = 255
			}
			var valid bool
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT nfo_commit_identity_valid($1,1)`, changed[:]).Scan(&valid); err != nil || valid != domain.ValidNFONativeIdentity(changed, 1) {
				t.Fatal("SQL and domain identity codec diverged", index, err)
			}
		}
	}
	for _, size := range []int{0, 1, 47, 49} {
		var valid bool
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT nfo_commit_identity_valid($1,1)`, make([]byte, size)).Scan(&valid); err != nil || valid {
			t.Fatal("SQL accepted malformed identity length", size, err)
		}
	}
}

func TestNFOCommitFilePersistenceNativeRejectsOtherSource(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("native evidence unsupported")
	}
	f, lease, _ := nfoCommitFixture(t)
	record, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1)
	if err != nil {
		t.Fatal(err)
	}
	other, service, scope, request := nfoWritePreparationFixture(t)
	if _, _, err := service.Prepare(other.ctx, other.a, "unrelated", request); err != nil {
		t.Fatal(err)
	}
	source, err := nfo.ReadSource(f.ctx, scope.Source.RootPath, scope.Source.RelativePath, request.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	writer, _ := nfo.NewWriterWithBudget(budget)
	if err := writer.StageCommitFiles(f.ctx, source, lease, record, f.s); !errors.Is(err, nfo.ErrChanged) {
		t.Fatal("unrelated source was attached to job intent", err)
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_file_plans`).Scan(&count); err != nil || count != 0 {
		t.Fatal("unrelated source persisted plan", err)
	}
	entries, err := filepath.Glob(filepath.Join(scope.Source.RootPath, ".jelee-nfo-commit-*"))
	if err != nil || len(entries) != 0 {
		t.Fatal("unrelated source received file side effects")
	}
}
