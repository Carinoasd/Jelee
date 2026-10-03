package postgres

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func mutateCommitCatalogScope(t *testing.T, f jobFixture, p domain.NFOWritePreparation, change string) {
	t.Helper()
	if change == "revision" {
		title := "new catalog revision"
		if value, err := f.s.UpdateItemMetadata(f.ctx, f.a, p.Scope.ItemID, p.Scope.Revision, []domain.ItemMetadataPatch{{Field: "title", Value: &title}}); err != nil || value.Revision != p.Scope.Revision+1 {
			t.Fatal("revision fixture", err)
		}
		return
	}
	queries := map[string]string{
		"generation":       `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`,
		"policy":           `UPDATE libraries SET nfo_mode='off',nfo_generation=nfo_generation+1 WHERE id=$1::uuid`,
		"root":             `UPDATE library_roots SET path=path||'/changed' WHERE library_id=$1::uuid`,
		"source_path":      `UPDATE media_sources SET relative_path='changed.mkv' WHERE library_id=$1::uuid`,
		"source_id":        `UPDATE media_sources SET id=gen_random_uuid() WHERE library_id=$1::uuid`,
		"source_missing":   `DELETE FROM media_sources WHERE library_id=$1::uuid`,
		"source_ambiguous": `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) SELECT item_id,library_id,root_id,'another.mkv',content_type FROM media_sources WHERE library_id=$1::uuid`,
		"kind":             `UPDATE items SET kind='HomeVideo' WHERE library_id=$1::uuid`,
	}
	if _, err := f.s.Pool.Exec(f.ctx, queries[change], p.Scope.LibraryID); err != nil {
		t.Fatal("scope fixture mutation", err)
	}
}

func TestNFOCommitCatalogScopeFencesBeginPlanReadyAndReplay(t *testing.T) {
	for _, phase := range []string{"begin", "plan", "ready", "plan_replay", "ready_replay"} {
		for _, change := range []string{"revision", "generation", "policy", "root", "source_path", "source_id", "source_missing", "source_ambiguous", "kind"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				f, l, p := nfoCommitFixture(t)
				var record domain.NFOWriteCommitRecord
				var err error
				plan, ready := commitPlanFixture(p), commitReadyFixture()
				wantJournal, wantPlan, wantReady := 0, 0, 0
				if phase != "begin" {
					record, err = f.s.BeginNFOWriteCommit(f.ctx, l, 1)
					if err != nil {
						t.Fatal(err)
					}
					wantJournal = 1
				}
				if phase == "ready" || phase == "plan_replay" || phase == "ready_replay" {
					if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, plan); err != nil {
						t.Fatal(err)
					}
					wantPlan = 1
				}
				if phase == "ready_replay" {
					if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, record.Token, ready); err != nil {
						t.Fatal(err)
					}
					wantReady = 1
				}
				mutateCommitCatalogScope(t, f, p, change)
				switch phase {
				case "begin":
					value, e := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
					err = e
					if value != (domain.NFOWriteCommitRecord{}) {
						t.Fatal("stale begin returned record")
					}
				case "plan", "plan_replay":
					value, e := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, record.Token, plan)
					err = e
					if value != (domain.NFOWriteCommitFilePlan{}) {
						t.Fatal("stale plan returned evidence")
					}
				default:
					value, e := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, record.Token, ready)
					err = e
					if value != (domain.NFOWriteCommitFilesReady{}) {
						t.Fatal("stale ready returned evidence")
					}
				}
				if err == nil {
					t.Fatal("changed catalog scope accepted")
				}
				var journals, plans, readies int
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_write_commit_journal),(SELECT count(*) FROM nfo_write_commit_file_plans),(SELECT count(*) FROM nfo_write_commit_files_ready)`).Scan(&journals, &plans, &readies); err != nil || journals != wantJournal || plans != wantPlan || readies != wantReady {
					t.Fatal("scope rejection did not roll back")
				}
				// Existing evidence is still observable for a future recovery audit;
				// the read port is not permission to execute a stale intent.
				if wantJournal == 1 {
					value, err := f.s.GetNFOWriteCommitFiles(f.ctx, l, 1, record.Token)
					if err != nil || value.Record != record {
						t.Fatal("stale retained evidence hidden", err)
					}
				}
			})
		}
	}
}

func TestNFOCommitCatalogScopeRejectsNativeStageAndReadyResume(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("native staging unsupported")
	}
	for _, phase := range []string{"before_plan", "ready_resume"} {
		for _, change := range []string{"revision", "generation", "policy", "root", "source_path", "source_id", "source_missing", "source_ambiguous", "kind"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				f, l, p := nfoCommitFixture(t)
				record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
				if err != nil {
					t.Fatal(err)
				}
				source, err := nfo.ReadSource(f.ctx, p.Scope.Source.RootPath, p.Scope.Source.RelativePath, p.Request.MaxBytes)
				if err != nil {
					t.Fatal(err)
				}
				b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
				w, _ := nfo.NewWriterWithBudget(b)
				if phase == "ready_resume" {
					if err := w.StageCommitFiles(f.ctx, source, l, record, f.s); err != nil {
						t.Fatal(err)
					}
				}
				mutateCommitCatalogScope(t, f, p, change)
				directory := filepath.Join(p.Scope.Source.RootPath, filepath.Dir(filepath.FromSlash(p.Scope.Source.RelativePath)))
				before := commitResumeFiles(t, directory)
				if err := w.StageCommitFiles(f.ctx, source, l, record, f.s); err == nil {
					t.Fatal("stale scope staged or resumed")
				}
				after := commitResumeFiles(t, directory)
				if len(before) != len(after) {
					t.Fatal("stale scope created or removed artifacts")
				}
				for name, data := range before {
					if !bytes.Equal(data, after[name]) {
						t.Fatal("stale scope changed file")
					}
				}
				if used, _ := b.PayloadBytes(); used != 0 || b.Stats() != (resources.Stats{}) {
					t.Fatal("rejection leaked resources")
				}
				entries, err := os.ReadDir(directory)
				if err != nil {
					t.Fatal(err)
				}
				if phase == "before_plan" && len(entries) != 1 {
					t.Fatal("plan rejection created native side effects")
				}
			})
		}
	}
}
