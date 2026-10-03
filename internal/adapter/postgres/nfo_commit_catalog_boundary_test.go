package postgres

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func TestNFOCommitCatalogScopeSupportsDirectoryIntents(t *testing.T) {
	for _, kind := range []string{"Series", "Season"} {
		t.Run(kind, func(t *testing.T) {
			f := newJobFixture(t)
			var root string
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&root); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "Series"), 0700); err != nil {
				t.Fatal(err)
			}
			series, err := f.s.ImportDirectory(f.ctx, "primary", root, "Series", "Series", "Series", "")
			if err != nil {
				t.Fatal(err)
			}
			item := series
			if kind == "Season" {
				if err := os.Mkdir(filepath.Join(root, "Series", "Season 1"), 0700); err != nil {
					t.Fatal(err)
				}
				item, err = f.s.ImportDirectory(f.ctx, "primary", root, "Series/Season 1", "Season 1", "Season", series)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='read-only',nfo_generation=2 WHERE id=$1::uuid`, f.registration.Library.ID); err != nil {
				t.Fatal(err)
			}
			scope, err := f.s.ResolveItemNFO(f.ctx, f.a, item, 1)
			if err != nil {
				t.Fatal(err)
			}
			tag := "tvshow"
			if kind == "Season" {
				tag = "season"
			}
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(scope.Source.RelativePath)), []byte("<"+tag+"><title>old</title></"+tag+">"), 0600); err != nil {
				t.Fatal(err)
			}
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
			preparer, _ := nfo.NewWritePreparer(budget)
			service, _ := app.NewNFOWritePreparations(f.s, preparer)
			p, _, err := service.Prepare(f.ctx, f.a, "directory-intent", domain.NFOWritePrepareRequest{ItemID: item, Revision: 1, MaxBytes: domain.NFODefaultSourceBytes, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "new"}}})
			if err != nil {
				t.Fatal(err)
			}
			job := nfoWriteJobFixture(t, f, p, "directory-job", domain.JobPriorityManual)
			lease := nfoWriteLeaseFixture(t, f, job.ID)
			record, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1)
			if err != nil {
				t.Fatal(err)
			}
			plan, ready := commitPlanFixture(p), commitReadyFixture()
			if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, 1, record.Token, plan); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, lease, 1, record.Token, ready); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE item_directory_sources SET relative_path=relative_path||'-changed' WHERE id=$1::uuid`, p.Scope.SourceID); err != nil {
				t.Fatal(err)
			}
			if value, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, lease, 1, record.Token, ready); err == nil || value != (domain.NFOWriteCommitFilesReady{}) {
				t.Fatal("changed directory scope replayed")
			}
			// Synthetic identities verify storage scope, not native authorization.
		})
	}
}

func TestNFOCommitCatalogScopeRechecksAfterStorageTrigger(t *testing.T) {
	for _, phase := range []string{"plan_insert", "plan_replay", "ready_insert", "ready_replay"} {
		t.Run(phase, func(t *testing.T) {
			f, lease, p := nfoCommitFixture(t)
			record, err := f.s.BeginNFOWriteCommit(f.ctx, lease, 1)
			if err != nil {
				t.Fatal(err)
			}
			plan, ready := commitPlanFixture(p), commitReadyFixture()
			wantPlans, wantReady := 0, 0
			if phase != "plan_insert" {
				if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, 1, record.Token, plan); err != nil {
					t.Fatal(err)
				}
				wantPlans = 1
			}
			if phase == "ready_replay" {
				if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, lease, 1, record.Token, ready); err != nil {
					t.Fatal(err)
				}
				wantReady = 1
			}
			table := "nfo_write_commit_file_plans"
			if phase == "ready_insert" || phase == "ready_replay" {
				table = "nfo_write_commit_files_ready"
			}
			query := fmt.Sprintf(`CREATE FUNCTION invalidate_commit_catalog_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=(SELECT j.library_id FROM jobs j JOIN nfo_write_commit_journal w ON w.job_id=j.id WHERE w.token=NEW.token); RETURN NEW; END $$; CREATE TRIGGER invalidate_commit_catalog_fixture AFTER INSERT OR UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION invalidate_commit_catalog_fixture()`, table)
			if _, err := f.s.Pool.Exec(f.ctx, query); err != nil {
				t.Fatal(err)
			}
			if table == "nfo_write_commit_file_plans" {
				value, e := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, 1, record.Token, plan)
				err = e
				if value != (domain.NFOWriteCommitFilePlan{}) {
					t.Fatal("changed scope returned plan")
				}
			} else {
				value, e := f.s.SaveNFOWriteCommitFilesReady(f.ctx, lease, 1, record.Token, ready)
				err = e
				if value != (domain.NFOWriteCommitFilesReady{}) {
					t.Fatal("changed scope returned ready")
				}
			}
			if err == nil {
				t.Fatal("post-write scope change committed")
			}
			var generation int64
			var plans, readies int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT nfo_generation,(SELECT count(*) FROM nfo_write_commit_file_plans),(SELECT count(*) FROM nfo_write_commit_files_ready) FROM libraries WHERE id=$1::uuid`, p.Scope.LibraryID).Scan(&generation, &plans, &readies); err != nil || generation != p.Scope.Generation || plans != wantPlans || readies != wantReady {
				t.Fatal("scope and storage writes did not roll back")
			}
		})
	}
}
