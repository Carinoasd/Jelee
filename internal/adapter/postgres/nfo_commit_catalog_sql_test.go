package postgres

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func execCommitCatalogFixture(f jobFixture, tx pgx.Tx, l domain.JobLease, r domain.NFOWriteCommitRecord, p domain.NFOWritePreparation, phase string) error {
	plan, ready := commitPlanFixture(p), commitReadyFixture()
	var err error
	switch phase {
	case "journal":
		_, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,1,$2,$3)`, l.Job.ID, l.Generation, l.Owner)
	case "journal_replay":
		_, err = tx.Exec(f.ctx, `UPDATE nfo_write_commit_journal SET token=token WHERE token=$1::uuid`, r.Token)
	case "plan":
		_, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_commit_file_plans(token,version,target_name,parent_identity,target_identity) VALUES($1::uuid,1,$2,$3,$4)`, r.Token, plan.TargetName, plan.ParentIdentity[:], plan.TargetIdentity[:])
	case "plan_replay":
		_, err = tx.Exec(f.ctx, `UPDATE nfo_write_commit_file_plans SET token=token WHERE token=$1::uuid`, r.Token)
	case "ready":
		_, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_commit_files_ready(token,output_identity,rollback_identity) VALUES($1::uuid,$2,$3)`, r.Token, ready.OutputIdentity[:], ready.RollbackIdentity[:])
	case "ready_replay":
		_, err = tx.Exec(f.ctx, `UPDATE nfo_write_commit_files_ready SET token=token WHERE token=$1::uuid`, r.Token)
	}
	return err
}

func catalogSQLPhaseFixture(t *testing.T, phase string) (jobFixture, domain.JobLease, domain.NFOWritePreparation, domain.NFOWriteCommitRecord) {
	t.Helper()
	f, l, p := nfoCommitFixture(t)
	var r domain.NFOWriteCommitRecord
	var err error
	if phase != "journal" {
		r, err = f.s.BeginNFOWriteCommit(f.ctx, l, 1)
		if err != nil {
			t.Fatal(err)
		}
	}
	if phase == "plan_replay" || phase == "ready" || phase == "ready_replay" {
		if _, err = f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, r.Token, commitPlanFixture(p)); err != nil {
			t.Fatal(err)
		}
	}
	if phase == "ready_replay" {
		if _, err = f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, r.Token, commitReadyFixture()); err != nil {
			t.Fatal(err)
		}
	}
	return f, l, p, r
}

func catalogSQLCounts(t *testing.T, f jobFixture) [3]int {
	t.Helper()
	var v [3]int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_commit_journal),(SELECT count(*) FROM nfo_write_commit_file_plans),(SELECT count(*) FROM nfo_write_commit_files_ready)`).Scan(&v[0], &v[1], &v[2]); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNFOCommitCatalogSQLRejectsStaleScope(t *testing.T) {
	for _, phase := range []string{"journal", "journal_replay", "plan", "plan_replay", "ready", "ready_replay"} {
		for _, change := range []string{"revision", "generation", "policy", "root", "source_path", "source_id", "source_missing", "source_ambiguous", "kind"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				f, l, p, r := catalogSQLPhaseFixture(t, phase)
				before := catalogSQLCounts(t, f)
				mutateCommitCatalogScope(t, f, p, change)
				tx, err := f.s.Pool.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(f.ctx)
				if err = execCommitCatalogFixture(f, tx, l, r, p, phase); err == nil {
					t.Fatal("direct SQL saved stale catalog scope")
				}
				_ = tx.Rollback(f.ctx)
				if catalogSQLCounts(t, f) != before {
					t.Fatal("rejected SQL changed retained evidence")
				}
			})
		}
	}
}

func TestNFOCommitCatalogSQLDeferredScope(t *testing.T) {
	for _, phase := range []string{"journal", "journal_replay", "plan", "plan_replay", "ready", "ready_replay"} {
		t.Run(phase, func(t *testing.T) {
			f, l, p, r := catalogSQLPhaseFixture(t, phase)
			// Isolate the deferred evidence trigger. The complete mutation guard
			// rejects this UPDATE earlier, covered separately below.
			if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE libraries DISABLE TRIGGER guard_nfo_catalog_library_mutation`); err != nil {
				t.Fatal(err)
			}
			before := catalogSQLCounts(t, f)
			tx, err := f.s.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			if err = execCommitCatalogFixture(f, tx, l, r, p, phase); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(f.ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.LibraryID); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(f.ctx); err == nil {
				t.Fatal("deferred catalog change committed")
			}
			if catalogSQLCounts(t, f) != before {
				t.Fatal("deferred rejection changed evidence")
			}
			var generation int64
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT nfo_generation FROM libraries WHERE id=$1::uuid`, p.Scope.LibraryID).Scan(&generation); err != nil || generation != p.Scope.Generation {
				t.Fatal("catalog mutation did not roll back")
			}
		})
	}
}

func TestNFOCommitCatalogSQLSnapshotPhantoms(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		for _, change := range []string{"source_ambiguous", "source_missing", "revision_sql"} {
			t.Run(string(isolation)+"/"+change, func(t *testing.T) {
				f, l, p, r := catalogSQLPhaseFixture(t, "plan")
				before := catalogSQLCounts(t, f)
				tx, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(f.ctx)
				var count int
				if err = tx.QueryRow(f.ctx, `SELECT count(*) FROM media_sources`).Scan(&count); err != nil || count != 1 {
					t.Fatal("snapshot fixture", err)
				}
				if change == "revision_sql" {
					if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, p.Scope.ItemID, p.Scope.Revision+1); err != nil {
						t.Fatal(err)
					}
				} else {
					mutateCommitCatalogScope(t, f, p, change)
				}
				if err = execCommitCatalogFixture(f, tx, l, r, p, "plan"); err == nil {
					err = tx.Commit(f.ctx)
				}
				if err == nil {
					t.Fatal("stale snapshot bypassed catalog fence")
				}
				_ = tx.Rollback(f.ctx)
				if catalogSQLCounts(t, f) != before {
					t.Fatal("snapshot rejection changed evidence")
				}
			})
		}
	}
}

func TestNFOCommitCatalogSQLAfterConstraintFlush(t *testing.T) {
	for _, phase := range []string{"journal", "journal_replay", "plan", "plan_replay", "ready", "ready_replay"} {
		for _, change := range []string{"generation", "root", "revision", "source_ambiguous"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				f, l, p, r := catalogSQLPhaseFixture(t, phase)
				before := catalogSQLCounts(t, f)
				tx, err := f.s.Pool.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(f.ctx)
				if err = execCommitCatalogFixture(f, tx, l, r, p, phase); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
					t.Fatal(err)
				}
				switch change {
				case "generation":
					_, err = tx.Exec(f.ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.LibraryID)
				case "root":
					_, err = tx.Exec(f.ctx, `UPDATE library_roots SET path=path||'/changed' WHERE id=$1::uuid`, p.Scope.RootID)
				case "revision":
					_, err = tx.Exec(f.ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, p.Scope.ItemID, p.Scope.Revision+1)
				case "source_ambiguous":
					_, err = tx.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) SELECT item_id,library_id,root_id,'another.mkv',content_type FROM media_sources WHERE item_id=$1::uuid`, p.Scope.ItemID)
				}
				if err == nil {
					err = tx.Commit(f.ctx)
				}
				if err == nil {
					t.Fatal("catalog mutation after constraint flush committed")
				}
				_ = tx.Rollback(f.ctx)
				if catalogSQLCounts(t, f) != before {
					t.Fatal("constraint flush rejection changed evidence")
				}
			})
		}
	}
}

func TestNFOCommitCatalogMigration(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newJobFixture(t)
		before := jobMetricMigrationStorage(t, f)
		jobMetricMigration(t, f, "down", 50)
		jobMetricMigration(t, f, "up", SchemaVersion)
		if f.s.Ready(f.ctx) != nil || jobMetricMigrationStorage(t, f) != before {
			t.Fatal("catalog migration changed epoch or readiness")
		}
	})
	t.Run("retained_journal", func(t *testing.T) {
		f, l, p, r := catalogSQLPhaseFixture(t, "plan")
		if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
			t.Fatal("retained catalog guard downgraded")
		}
		version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
		if err != nil || version != 50 || !dirty || f.s.Ready(f.ctx) == nil {
			t.Fatal("retained downgrade did not reject dirty schema")
		}
		var token string
		if err = f.s.Pool.QueryRow(f.ctx, `SELECT token::text FROM nfo_write_commit_journal WHERE job_id=$1::uuid`, l.Job.ID).Scan(&token); err != nil || token != r.Token || p.Scope.ItemID == "" {
			t.Fatal("retained downgrade lost journal")
		}
	})
	t.Run("historical_scope", func(t *testing.T) {
		f, l, p := nfoCommitFixture(t)
		jobMetricMigration(t, f, "down", 50)
		r, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
		if err != nil {
			t.Fatal(err)
		}
		mutateCommitCatalogScope(t, f, p, "generation")
		jobMetricMigration(t, f, "up", SchemaVersion)
		e, err := f.s.GetNFOWriteCommitFiles(f.ctx, l, 1, r.Token)
		if err != nil || e.Record.Token != r.Token {
			t.Fatal("upgrade hid historical journal", err)
		}
		if v, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, r.Token, commitPlanFixture(p)); err == nil || v != (domain.NFOWriteCommitFilePlan{}) {
			t.Fatal("upgrade authorized historical scope")
		}
	})
}
