//go:build windows || linux

package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type retainedStagingRepository struct {
	*stagingRepository
	mu       sync.Mutex
	evidence domain.NFOWriteCommitFileEvidence
	lost     bool
}

func (r *retainedStagingRepository) GetNFOWriteCommitFiles(_ context.Context, l domain.JobLease, seq int, token string) (domain.NFOWriteCommitFileEvidence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := r.evidence
	value.Record = domain.NFOWriteCommitRecord{JobID: l.Job.ID, Owner: l.Owner, Generation: l.Generation, Sequence: seq, Token: token}
	return value, nil
}
func (r *retainedStagingRepository) SaveNFOWriteCommitFilePlan(_ context.Context, _ domain.JobLease, _ int, _ string, p domain.NFOWriteCommitFilePlan) (domain.NFOWriteCommitFilePlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plans.Add(1)
	if r.evidence.PlanRecorded && r.evidence.Plan != p {
		return domain.NFOWriteCommitFilePlan{}, domain.ErrConflict
	}
	r.evidence.PlanRecorded = true
	r.evidence.Plan = p
	return p, nil
}
func (r *retainedStagingRepository) SaveNFOWriteCommitFilesReady(_ context.Context, _ domain.JobLease, _ int, _ string, p domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readies.Add(1)
	if r.evidence.ReadyRecorded && r.evidence.Ready != p {
		return domain.NFOWriteCommitFilesReady{}, domain.ErrConflict
	}
	r.evidence.ReadyRecorded = true
	r.evidence.Ready = p
	if r.lost {
		r.lost = false
		return domain.NFOWriteCommitFilesReady{}, domain.ErrDatabase
	}
	return p, nil
}

func TestStageResumeRetainedReadyAndRejectsMissingProof(t *testing.T) {
	for _, reason := range []string{"ready", "output_missing", "rollback_replaced", "target_output", "foreign_plan"} {
		t.Run(reason, func(t *testing.T) {
			root, source, p, b := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
			l, record := stagingLease()
			w, _ := NewWriterWithBudget(b)
			repo := &retainedStagingRepository{stagingRepository: &stagingRepository{read: func(_ context.Context, l domain.JobLease, seq int) (domain.NFOWriteTask, error) {
				return domain.NFOWriteTask{JobID: l.Job.ID, Sequence: seq, Preparation: p}, nil
			}}, lost: true}
			if err := w.StageCommitFiles(context.Background(), source, l, record, repo); !errors.Is(err, ErrReplace) {
				t.Fatal("unknown response", err)
			}
			prefix := ".jelee-nfo-commit-" + strings.ReplaceAll(record.Token, "-", "") + "-"
			var err error
			switch reason {
			case "output_missing":
				err = os.Remove(filepath.Join(root, prefix+"output"))
			case "rollback_replaced":
				err = os.Remove(filepath.Join(root, prefix+"rollback"))
				if err == nil {
					err = os.WriteFile(filepath.Join(root, prefix+"rollback"), p.Original, 0600)
				}
			case "target_output":
				err = os.WriteFile(filepath.Join(root, source.relative), p.Replacement, 0600)
			case "foreign_plan":
				repo.evidence.Plan.TargetName = "foreign.nfo"
			}
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := ReadSource(context.Background(), root, source.relative, p.Request.MaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			freshWriter, _ := NewWriterWithBudget(b)
			err = freshWriter.StageCommitFiles(context.Background(), fresh, l, record, repo)
			if reason == "ready" {
				if err != nil || repo.plans.Load() != 2 || repo.readies.Load() != 2 {
					t.Fatal("ready resume failed or skipped lease replay", err)
				}
			} else if err == nil {
				t.Fatal("missing or foreign proof accepted")
			}
			if used, _ := b.PayloadBytes(); used != 0 {
				t.Fatal("resume leaked bytes")
			}
		})
	}
}
