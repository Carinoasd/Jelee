//go:build linux || windows

package nfo

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func TestStageNativeReceiptRejectsNewMediaAndAncestorBeforePlanAndResume(t *testing.T) {
	for _, phase := range []string{"before_plan", "ready_resume"} {
		for _, change := range []string{"missing_receipt", "media", "absolute_ancestor"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				scope := nativePreparationSourceFixture(t, false)
				b := writerBudget(t, 1, 0)
				preparer, _ := NewWritePreparer(b)
				request := domain.NFOWritePrepareRequest{ItemID: scope.ItemID, Revision: 1, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "new"}}, MaxBytes: 1024, Backups: 1}
				prepared, err := preparer.PrepareNFOWrite(context.Background(), scope, request)
				if err != nil {
					t.Fatal("prepare owned native stage fixture")
				}
				source, err := ReadSource(context.Background(), scope.Source.RootPath, scope.Source.RelativePath, request.MaxBytes)
				if err != nil {
					t.Fatal("read owned native stage source")
				}
				lease, record := stagingLease()
				repo := &retainedStagingRepository{stagingRepository: &stagingRepository{read: func(context.Context, domain.JobLease, int) (domain.NFOWriteTask, error) {
					return domain.NFOWriteTask{JobID: lease.Job.ID, Sequence: 1, Preparation: prepared}, nil
				}}}
				writer, _ := NewWriterWithBudget(b)
				if phase == "ready_resume" {
					if err := writer.StageCommitFiles(context.Background(), source, lease, record, repo); err != nil {
						t.Fatal("stage owned native evidence", err)
					}
				}
				switch change {
				case "missing_receipt":
					prepared.NativeObservation = domain.NFONativePreparationReceipt{}
				case "media":
					media := filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.MediaPath))
					if err := os.Rename(media, media+".retained"); err != nil {
						t.Fatal("retain original media")
					}
					if err := os.WriteFile(media, []byte("owned media"), 0600); err != nil {
						t.Fatal("create new physical media")
					}
				case "absolute_ancestor":
					parent := filepath.Dir(scope.Source.RootPath)
					if err := os.Rename(parent, parent+".retained"); err != nil {
						t.Fatal("retain original absolute ancestor")
					}
					if err := os.Mkdir(parent, 0700); err != nil {
						t.Fatal("create new absolute ancestor")
					}
					if err := os.Rename(filepath.Join(parent+".retained", filepath.Base(scope.Source.RootPath)), scope.Source.RootPath); err != nil {
						t.Fatal("preserve original root and file objects")
					}
				}
				snapshot := func() map[string][]byte {
					files := map[string][]byte{}
					if err := filepath.WalkDir(scope.Source.RootPath, func(path string, entry fs.DirEntry, err error) error {
						if err != nil {
							return err
						}
						if !entry.IsDir() {
							data, e := os.ReadFile(path)
							if e != nil {
								return e
							}
							relative, _ := filepath.Rel(scope.Source.RootPath, path)
							files[relative] = data
						}
						return nil
					}); err != nil {
						t.Fatal("snapshot owned stage files")
					}
					return files
				}
				before := snapshot()
				plans, readies := repo.plans.Load(), repo.readies.Load()
				if err := writer.StageCommitFiles(context.Background(), source, lease, record, repo); err == nil {
					t.Fatal("changed native scope authorized stage or resume")
				}
				after := snapshot()
				if len(before) != len(after) || repo.plans.Load() != plans || repo.readies.Load() != readies || b.Stats() != (resources.Stats{}) {
					t.Fatal("rejection changed native evidence or leaked permits")
				}
				for name, data := range before {
					if !bytes.Equal(data, after[name]) {
						t.Fatal("rejection changed retained artifact")
					}
				}
				if used, _ := b.PayloadBytes(); used != 0 {
					t.Fatal("rejection leaked payload reservation")
				}
			})
		}
	}
}
