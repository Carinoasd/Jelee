package postgres

import (
	"github.com/MoYuanCN/Jelee/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestNFOCommitCatalogSavepoints(t *testing.T) {
	for _, phase := range []string{"journal", "journal_replay", "plan", "plan_replay", "ready", "ready_replay"} {
		for _, mode := range []string{"active", "released"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				f, l, p, r := catalogSQLPhaseFixture(t, phase)
				before := catalogSQLCounts(t, f)
				tx, err := f.s.Pool.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(f.ctx)
				if _, err = tx.Exec(f.ctx, `SAVEPOINT nfo_owned`); err != nil {
					t.Fatal(err)
				}
				if err = execCommitCatalogFixture(f, tx, l, r, p, phase); err != nil {
					t.Fatal(err)
				}
				if mode == "released" {
					if _, err = tx.Exec(f.ctx, `RELEASE SAVEPOINT nfo_owned`); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
					t.Fatal(err)
				}
				_, err = tx.Exec(f.ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.LibraryID)
				if err == nil {
					err = tx.Commit(f.ctx)
				}
				if err == nil {
					t.Fatal("savepoint evidence bypassed catalog mutation guard")
				}
				_ = tx.Rollback(f.ctx)
				if catalogSQLCounts(t, f) != before {
					t.Fatal("savepoint rejection changed evidence")
				}
			})
		}
	}
}

func TestNFOCommitCatalogSeriesSourceSwitch(t *testing.T) {
	f, service, scope, request := nfoWritePreparationFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE items SET kind='Series' WHERE id=$1::uuid`, scope.ItemID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope.Source.RootPath, scope.Source.RelativePath), []byte("<tvshow><title>old</title></tvshow>"), 0600); err != nil {
		t.Fatal(err)
	}
	p, _, err := service.Prepare(f.ctx, f.a, "series-switch", request)
	if err != nil {
		t.Fatal(err)
	}
	job := nfoWriteJobFixture(t, f, p, "series-switch-job", domain.JobPriorityManual)
	l := nfoWriteLeaseFixture(t, f, job.ID)
	r, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO item_directory_sources(item_id,library_id,kind,root_id,relative_path) VALUES($1::uuid,$2::uuid,'Series',$3::uuid,'Series')`, scope.ItemID, scope.LibraryID, scope.RootID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if err = execCommitCatalogFixture(f, tx, l, r, p, "plan"); err == nil {
		err = tx.Commit(f.ctx)
	}
	if err == nil {
		t.Fatal("Series directory source superseded fixed media intent")
	}
}

func TestNFOCommitCatalogSavepointRollback(t *testing.T) {
	for _, phase := range []string{"journal", "journal_replay", "plan", "plan_replay", "ready", "ready_replay"} {
		t.Run(phase, func(t *testing.T) {
			f, l, p, r := catalogSQLPhaseFixture(t, phase)
			before := catalogSQLCounts(t, f)
			tx, err := f.s.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			if _, err = tx.Exec(f.ctx, `SAVEPOINT nfo_owned`); err != nil {
				t.Fatal(err)
			}
			if err = execCommitCatalogFixture(f, tx, l, r, p, phase); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(f.ctx, `ROLLBACK TO SAVEPOINT nfo_owned`); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(f.ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.LibraryID); err != nil {
				t.Fatal("rolled back evidence froze catalog", err)
			}
			if err = tx.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			if catalogSQLCounts(t, f) != before {
				t.Fatal("rolled back savepoint changed evidence")
			}
		})
	}
}

func TestNFOCommitCatalogManySavepoints(t *testing.T) {
	for _, phase := range []string{"plan_replay", "ready_replay"} {
		for _, mode := range []string{"active", "released"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				f, l, p, r := catalogSQLPhaseFixture(t, phase)
				before := catalogSQLCounts(t, f)
				tx, err := f.s.Pool.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(f.ctx)
				for i := 0; i < 72; i++ {
					if _, err = tx.Exec(f.ctx, `SAVEPOINT nfo_owned`); err != nil {
						t.Fatal(err)
					}
					if err = execCommitCatalogFixture(f, tx, l, r, p, phase); err != nil {
						t.Fatal(err)
					}
					if mode == "released" {
						if _, err = tx.Exec(f.ctx, `RELEASE SAVEPOINT nfo_owned`); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, err = tx.Exec(f.ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
					t.Fatal(err)
				}
				_, err = tx.Exec(f.ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, p.Scope.LibraryID)
				if err == nil {
					err = tx.Commit(f.ctx)
				}
				if err == nil {
					t.Fatal("many savepoints bypassed catalog mutation guard")
				}
				_ = tx.Rollback(f.ctx)
				if catalogSQLCounts(t, f) != before {
					t.Fatal("many savepoints changed evidence")
				}
			})
		}
	}
}
