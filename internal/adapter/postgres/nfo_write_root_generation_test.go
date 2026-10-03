package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Simulate data created before schema52 solely in an owned migration fixture.
// Published old guards are tested at their original schema, independently of
// the new retained-proof refusal. Never discard unresolved journal evidence.
func nfoRootGenerationLegacyAt51(t *testing.T, f jobFixture) {
	t.Helper()
	var journals int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal`).Scan(&journals); err != nil || journals != 0 {
		t.Fatal("legacy fixture cannot discard unresolved evidence", err)
	}
	_, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE nfo_write_preparations DISABLE TRIGGER ALL;
ALTER TABLE nfo_write_entries DISABLE TRIGGER ALL;
UPDATE nfo_write_preparations SET root_generation=NULL;
UPDATE nfo_write_entries SET root_generation=NULL;
ALTER TABLE nfo_write_entries ENABLE TRIGGER ALL;
ALTER TABLE nfo_write_preparations ENABLE TRIGGER ALL;`)
	if err != nil {
		t.Fatal(err)
	}
	jobMetricMigration(t, f, "down", 51)
}

func TestNFOWriteRootGenerationSQLCommitFence(t *testing.T) {
	for _, phase := range []string{"journal", "journal_replay", "plan", "plan_replay", "ready", "ready_replay"} {
		t.Run(phase, func(t *testing.T) {
			f, l, p, r := catalogSQLPhaseFixture(t, phase)
			before := catalogSQLCounts(t, f)
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.RootID); err != nil {
				t.Fatal(err)
			}
			tx, err := f.s.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			if err = execCommitCatalogFixture(f, tx, l, r, p, phase); err == nil {
				t.Fatal("root generation drift bypassed SQL commit fence")
			}
			_ = tx.Rollback(f.ctx)
			if catalogSQLCounts(t, f) != before {
				t.Fatal("rejected root generation changed evidence")
			}
		})
	}
}

func TestNFOWriteRootGenerationApplicationFence(t *testing.T) {
	for _, phase := range []string{"journal", "journal_replay", "plan", "plan_replay", "ready", "ready_replay", "stage", "resume"} {
		t.Run(phase, func(t *testing.T) {
			fixturePhase := phase
			if phase == "stage" || phase == "resume" {
				fixturePhase = "plan"
			}
			f, l, p, r := catalogSQLPhaseFixture(t, fixturePhase)
			if p.Scope.RootGeneration < 1 {
				t.Fatal("prepare omitted root generation")
			}
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
			writer, _ := nfo.NewWriterWithBudget(budget)
			source, err := nfo.ReadSource(f.ctx, p.Scope.Source.RootPath, p.Scope.Source.RelativePath, p.Request.MaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "resume" {
				if err = writer.StageCommitFiles(f.ctx, source, l, r, f.s); err != nil {
					t.Fatal(err)
				}
			}
			before := catalogSQLCounts(t, f)
			files := commitResumeFiles(t, p.Scope.Source.RootPath)
			if _, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.RootID); err != nil {
				t.Fatal(err)
			}
			switch phase {
			case "journal", "journal_replay":
				v, e := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
				err = e
				if v != (domain.NFOWriteCommitRecord{}) {
					t.Fatal("rejected begin leaked result")
				}
			case "plan", "plan_replay":
				v, e := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, r.Token, commitPlanFixture(p))
				err = e
				if v != (domain.NFOWriteCommitFilePlan{}) {
					t.Fatal("rejected plan leaked result")
				}
			case "ready", "ready_replay":
				v, e := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, r.Token, commitReadyFixture())
				err = e
				if v != (domain.NFOWriteCommitFilesReady{}) {
					t.Fatal("rejected ready leaked result")
				}
			default:
				err = writer.StageCommitFiles(f.ctx, source, l, r, f.s)
			}
			if err == nil {
				t.Fatal("root generation drift bypassed application fence")
			}
			if catalogSQLCounts(t, f) != before {
				t.Fatal("rejection changed evidence")
			}
			after := commitResumeFiles(t, p.Scope.Source.RootPath)
			if len(after) != len(files) {
				t.Fatal("rejection changed native file count")
			}
			for name, data := range files {
				if !bytes.Equal(data, after[name]) {
					t.Fatal("rejection changed remaining bytes")
				}
			}
			if budget.Stats() != (resources.Stats{}) {
				t.Fatal("rejection leaked permits")
			}
		})
	}
}

func TestNFOWriteRootGenerationAfterConstraintFlush(t *testing.T) {
	for _, phase := range []string{"journal", "journal_replay", "plan", "plan_replay", "ready", "ready_replay"} {
		for _, released := range []bool{false, true} {
			name := phase + "/active"
			if released {
				name = phase + "/released"
			}
			t.Run(name, func(t *testing.T) {
				f, l, p, r := catalogSQLPhaseFixture(t, phase)
				before := catalogSQLCounts(t, f)
				tx, err := f.s.Pool.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(f.ctx)
				if _, err = tx.Exec(f.ctx, `SAVEPOINT root_observation`); err != nil {
					t.Fatal(err)
				}
				if err = execCommitCatalogFixture(f, tx, l, r, p, phase); err != nil {
					t.Fatal(err)
				}
				if released {
					if _, err = tx.Exec(f.ctx, `RELEASE SAVEPOINT root_observation`); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(f.ctx, `UPDATE library_roots SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.RootID); err == nil {
					t.Fatal("root drift after flushed savepoint committed")
				}
				_ = tx.Rollback(f.ctx)
				if catalogSQLCounts(t, f) != before {
					t.Fatal("rejection changed evidence")
				}
			})
		}
	}
}

func TestNFOWriteRootGenerationPreparationAndCopy(t *testing.T) {
	t.Run("save", func(t *testing.T) {
		f, _, scope, request := nfoWritePreparationFixture(t)
		budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
		preparer, _ := nfo.NewWritePreparer(budget)
		p, err := preparer.PrepareNFOWrite(f.ctx, scope, request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, scope.RootID); err != nil {
			t.Fatal(err)
		}
		if v, replay, err := f.s.SaveNFOWritePreparation(f.ctx, f.a, "stale-root", p); err == nil || replay || v.ID != "" {
			t.Fatal("stale root preparation saved")
		}
		var count int
		if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_preparations`).Scan(&count); err != nil || count != 0 {
			t.Fatal("rejected preparation retained data", err)
		}
	})
	t.Run("copy", func(t *testing.T) {
		f, service, _, request := nfoWritePreparationFixture(t)
		p, _, err := service.Prepare(f.ctx, f.a, "copy-root", request)
		if err != nil {
			t.Fatal(err)
		}
		j := nfoWriteJobFixture(t, f, p, "first-job", domain.JobPriorityManual)
		if _, err = f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.RootID); err != nil {
			t.Fatal(err)
		}
		tx, err := f.s.Pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(f.ctx)
		var second string
		if err = tx.QueryRow(f.ctx, `INSERT INTO jobs SELECT (jsonb_populate_record(NULL::jobs,to_jsonb(j)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key','second-job','state','queued','cancel_requested',false,'finished_at',NULL))).* FROM jobs j WHERE id=$1::uuid RETURNING id::text`, j.ID).Scan(&second); err != nil {
			t.Fatal(err)
		}
		if err = copyNFOWriteIntents(f.ctx, tx, second, []string{p.ID}); err == nil {
			t.Fatal("stale root copied into new job")
		}
		_ = tx.Rollback(f.ctx)
		var count int
		if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs WHERE kind='nfo_write'`).Scan(&count); err != nil || count != 1 {
			t.Fatal("rejected copy left partial job", err)
		}
	})
}

func TestNFOWriteRootGenerationMigration(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newJobFixture(t)
		before := jobMetricMigrationStorage(t, f)
		jobMetricMigration(t, f, "down", 51)
		jobMetricMigration(t, f, "up", SchemaVersion)
		if f.s.Ready(f.ctx) != nil || jobMetricMigrationStorage(t, f) != before {
			t.Fatal("root migration changed epoch")
		}
	})
	t.Run("retained", func(t *testing.T) {
		f, service, _, request := nfoWritePreparationFixture(t)
		p, _, err := service.Prepare(f.ctx, f.a, "retained-root", request)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
			t.Fatal("downgrade discarded root proof")
		}
		v, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
		if err != nil || v != 51 || !dirty {
			t.Fatal("retained proof did not retain dirty51", err)
		}
		digest, _ := domain.NFOWriteRequestDigest(request)
		old, err := f.s.FindNFOWritePreparation(f.ctx, f.a, "retained-root", digest)
		if err != nil || old.ID != p.ID || old.Scope.RootGeneration != p.Scope.RootGeneration {
			t.Fatal("refused downgrade lost observation", err)
		}
	})
	t.Run("historical", func(t *testing.T) {
		f, l, p := nfoCommitFixture(t)
		nfoRootGenerationLegacyAt51(t, f)
		jobMetricMigration(t, f, "up", SchemaVersion)
		task, err := f.s.GetNFOWriteTask(f.ctx, l, 1)
		if err != nil || task.Preparation.Scope.RootGeneration != 0 || !bytes.Equal(task.Preparation.Original, p.Original) {
			t.Fatal("upgrade backfilled or hid historical observation", err)
		}
		if r, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1); err == nil || r != (domain.NFOWriteCommitRecord{}) {
			t.Fatal("historical missing proof authorized begin")
		}
		tx, err := f.s.Pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(f.ctx)
		if err = execCommitCatalogFixture(f, tx, l, domain.NFOWriteCommitRecord{}, p, "journal"); err == nil {
			t.Fatal("SQL accepted missing historical proof")
		}
		_ = tx.Rollback(f.ctx)
		if catalogSQLCounts(t, f) != ([3]int{}) {
			t.Fatal("historical rejection created evidence")
		}
		if _, err = os.Stat(filepath.Join(p.Scope.Source.RootPath, p.Scope.Source.RelativePath)); err != nil {
			t.Fatal("historical rejection changed target")
		}
	})
}

func TestNFOWriteRootGenerationSnapshot(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f, l, p, r := catalogSQLPhaseFixture(t, "plan")
			tx, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			var old int64
			if err = tx.QueryRow(f.ctx, `SELECT nfo_generation FROM library_roots WHERE id=$1::uuid`, p.Scope.RootID).Scan(&old); err != nil || old != p.Scope.RootGeneration {
				t.Fatal("snapshot differs", err)
			}
			if _, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.RootID); err != nil {
				t.Fatal(err)
			}
			if err = execCommitCatalogFixture(f, tx, l, r, p, "plan"); err == nil {
				t.Fatal("old snapshot accepted stale root generation")
			}
			_ = tx.Rollback(f.ctx)
			if catalogSQLCounts(t, f) != ([3]int{1, 0, 0}) {
				t.Fatal("snapshot rejection changed evidence")
			}
		})
	}
}

func TestNFOWriteRootGenerationPreparationSQL(t *testing.T) {
	for _, mode := range []string{"missing", "zero", "stale", "immutable", "deferred", "flushed"} {
		t.Run(mode, func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			p, _, err := service.Prepare(f.ctx, f.a, "prepared-root", request)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.s.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			switch mode {
			case "missing", "zero", "stale":
				patch := map[string]string{"missing": "NULL", "zero": "0", "stale": "root_generation+1"}[mode]
				_, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_preparations SELECT (jsonb_populate_record(NULL::nfo_write_preparations,to_jsonb(p)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key','invalid-root','root_generation',`+patch+`))).* FROM nfo_write_preparations p WHERE id=$1::uuid`, p.ID)
			case "immutable":
				_, err = tx.Exec(f.ctx, `UPDATE nfo_write_preparations SET root_generation=root_generation+1 WHERE id=$1::uuid`, p.ID)
			default:
				if mode == "deferred" {
					if _, err = tx.Exec(f.ctx, `ALTER TABLE library_roots DISABLE TRIGGER guard_nfo_write_root_mutation`); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = tx.Exec(f.ctx, cloneQuotaPreparation, p.ID, "clone-root"); err != nil {
					t.Fatal(err)
				}
				if mode == "flushed" {
					if _, err = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
						t.Fatal(err)
					}
				}
				_, err = tx.Exec(f.ctx, `UPDATE library_roots SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.RootID)
				if mode == "deferred" {
					if err != nil {
						t.Fatal("isolated deferred mutation", err)
					}
					err = tx.Commit(f.ctx)
				}
			}
			if err == nil {
				t.Fatal("SQL accepted invalid or changed root observation")
			}
			_ = tx.Rollback(f.ctx)
			var count int
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_preparations`).Scan(&count); err != nil || count != 1 {
				t.Fatal("root SQL rejection changed retained rows", err)
			}
		})
	}
}

func TestNFOWriteRootGenerationSaveLockOrder(t *testing.T) {
	f, _, scope, request := nfoWritePreparationFixture(t)
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	preparer, _ := nfo.NewWritePreparer(budget)
	p, err := preparer.PrepareNFOWrite(f.ctx, scope, request)
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(f.ctx)
	if _, err = first.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()),17481247); UPDATE nfo_write_quota_fences SET scope=scope WHERE scope='preparation'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, e := f.s.SaveNFOWritePreparation(ctx, f.a, "lock-order-root", p); done <- e }()
	waiting := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if err = f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_locks WHERE locktype='advisory' AND NOT granted AND classid=hashtext(current_schema())::oid AND objid=17481247)`).Scan(&waiting); err != nil {
			break
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var generation int64
	var rootErr error
	if waiting {
		rootErr = first.QueryRow(f.ctx, `SELECT nfo_generation FROM library_roots WHERE id=$1::uuid FOR UPDATE NOWAIT`, scope.RootID).Scan(&generation)
	}
	_ = first.Rollback(f.ctx)
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal("quota waiter did not join")
	}
	if !waiting {
		t.Fatal("save never waited for owned quota lock")
	}
	if rootErr != nil {
		t.Fatal("quota waiter already locked root", rootErr)
	}
	if generation != scope.RootGeneration || err != nil {
		t.Fatal("ordered save failed", err)
	}
}

func TestNFOWriteRootGenerationCounterCannotRegress(t *testing.T) {
	f, l, p := nfoCommitFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.RootID); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET nfo_generation=$2 WHERE id=$1::uuid`, p.Scope.RootID, p.Scope.RootGeneration)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "nfo root generation cannot regress" {
		t.Fatal("root counter regression was not refused", err)
	}
	if r, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1); err == nil || r != (domain.NFOWriteCommitRecord{}) {
		t.Fatal("regressed counter revived an old intent")
	}
	if catalogSQLCounts(t, f) != ([3]int{}) {
		t.Fatal("counter regression created evidence")
	}
}

func TestNFOWriteRootGenerationPreparedRootChangeAfterFlush(t *testing.T) {
	for _, mode := range []string{"generation", "path", "identity", "delete"} {
		t.Run(mode, func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			p, _, err := service.Prepare(f.ctx, f.a, "root-mutation-base", request)
			if err != nil {
				t.Fatal(err)
			}
			// Isolate the preparation root guard from media FK rejection. These
			// SQL-only clones are storage evidence and never reach a writer.
			if _, err = f.s.Pool.Exec(f.ctx, `DELETE FROM media_sources WHERE item_id=$1::uuid`, p.Scope.ItemID); err != nil {
				t.Fatal(err)
			}
			tx, err := f.s.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			if _, err = tx.Exec(f.ctx, cloneQuotaPreparation, p.ID, "root-mutation-clone"); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
				t.Fatal(err)
			}
			query := map[string]string{"generation": `UPDATE library_roots SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, "path": `UPDATE library_roots SET path=path||'/changed' WHERE id=$1::uuid`, "identity": `UPDATE library_roots SET id=gen_random_uuid() WHERE id=$1::uuid`, "delete": `DELETE FROM library_roots WHERE id=$1::uuid`}[mode]
			_, err = tx.Exec(f.ctx, query, p.Scope.RootID)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "nfo write root changed after observation" {
				t.Fatal("prepared root mutation bypassed guard or unrelated refusal", err)
			}
			_ = tx.Rollback(f.ctx)
			var count int
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_preparations`).Scan(&count); err != nil || count != 1 {
				t.Fatal("root mutation changed retained preparation", err)
			}
		})
	}
}
