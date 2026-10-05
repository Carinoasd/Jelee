package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// lostFirstCheckpointStore leaves every name created before a first checkpoint
// without durable evidence, as an abrupt stop before that commit would.
type lostFirstCheckpointStore struct{ *Store }

func (lostFirstCheckpointStore) SaveNFOWriteCommitFileCheckpoint(context.Context, domain.JobLease, int, string, domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	return domain.NFOWriteCommitFileCheckpoint{}, domain.ErrDatabase
}
func (lostFirstCheckpointStore) SaveNFOWriteCommitAttemptCheckpoint(context.Context, domain.JobLease, int, string, uint8, domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	return domain.NFOWriteCommitFileCheckpoint{}, domain.ErrDatabase
}

func TestNFOCommitAttemptStageTruePG(t *testing.T) {
	for _, mode := range []string{"rotate", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			f, l, p := nfoCommitFixture(t)
			record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
			if err != nil {
				t.Fatal("owned stage journal unavailable")
			}
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
			stage := func(repository app.NFOWriteCommitAttemptStageRepository) error {
				source, err := nfo.ReadSource(f.ctx, p.Scope.Source.RootPath, p.Scope.Source.RelativePath, p.Request.MaxBytes)
				if err != nil {
					t.Fatal("owned stage source unavailable")
				}
				writer, _ := nfo.NewWriterWithBudget(budget)
				return writer.StageCommitFiles(f.ctx, source, l, record, repository)
			}
			directory := filepath.Dir(filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath)))
			name := func(attempt int) string {
				base := ".jelee-nfo-commit-" + strings.ReplaceAll(record.Token, "-", "")
				if attempt != 0 {
					base += "-attempt-" + string(rune('0'+attempt))
				}
				return filepath.Join(directory, base+"-output")
			}
			allocated := func() int {
				var n int
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_attempts WHERE token=$1::uuid AND attempt>0`, record.Token).Scan(&n); err != nil {
					t.Fatal("owned attempt count unavailable")
				}
				return n
			}
			lost := lostFirstCheckpointStore{f.s}
			if err := stage(lost); !errors.Is(err, nfo.ErrReplace) {
				t.Fatal("lost first checkpoint not reported as unknown")
			}
			if mode == "rotate" {
				if err := stage(f.s); err != nil {
					t.Fatal("true PG stage did not rotate past unknown names")
				}
				attempts, err := f.s.GetNFOWriteCommitAttempts(f.ctx, l, 1, record.Token)
				if err != nil || allocated() != 1 || !attempts.Attempts[0].ReadyRecorded || attempts.Attempts[0].Checkpoint.Phase != 2 {
					t.Fatal("attempt one not durably selected")
				}
				if err := stage(f.s); err != nil || allocated() != 1 {
					t.Fatal("ready attempt replay failed or reallocated")
				}
			} else {
				for attempt := 1; attempt <= 3; attempt++ {
					if err := stage(lost); !errors.Is(err, nfo.ErrReplace) || allocated() != attempt {
						t.Fatal("bounded rotation did not allocate in order")
					}
				}
				if err := stage(f.s); !errors.Is(err, nfo.ErrCommitAttemptsExhausted) || allocated() != 3 {
					t.Fatal("exhausted namespaces not refused within bound")
				}
			}
			last := 1
			if mode == "exhausted" {
				last = 3
			}
			for attempt := 0; attempt <= last; attempt++ {
				if _, err := os.Lstat(name(attempt)); err != nil {
					t.Fatal("retained or selected namespace object missing")
				}
			}
			evidence, err := f.s.GetNFOWriteCommitFiles(f.ctx, l, 1, record.Token)
			if err != nil || !evidence.PlanRecorded || evidence.CheckpointRecorded || evidence.ReadyRecorded {
				t.Fatal("legacy evidence written beside an allocated attempt")
			}
			data, err := os.ReadFile(filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath)))
			if err != nil || string(data) != string(p.Original) {
				t.Fatal("attempt stage changed target")
			}
		})
	}
}
