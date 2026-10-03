package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func TestNFOCommitFileEvidenceReadFencesAndNoCreation(t *testing.T) {
	for _, reason := range []string{"missing", "plan_only", "ready", "owner", "generation", "expiry", "cancel", "disabled", "not_admin", "token", "sequence", "job"} {
		t.Run(reason, func(t *testing.T) {
			f, l, p := nfoCommitFixture(t)
			token := "a0000000-0000-0000-0000-000000000021"
			var record domain.NFOWriteCommitRecord
			var err error
			if reason != "missing" {
				record, err = f.s.BeginNFOWriteCommit(f.ctx, l, 1)
				if err != nil {
					t.Fatal(err)
				}
				token = record.Token
				if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, l, 1, token, commitPlanFixture(p)); err != nil {
					t.Fatal(err)
				}
				if reason != "plan_only" {
					if _, err := f.s.SaveNFOWriteCommitFilesReady(f.ctx, l, 1, token, commitReadyFixture()); err != nil {
						t.Fatal(err)
					}
				}
			}
			sequence := 1
			switch reason {
			case "owner":
				l.Owner = "foreign-owner"
			case "generation":
				l.Generation++
			case "expiry":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID)
			case "cancel":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l.Job.ID)
			case "disabled":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID)
			case "not_admin":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, f.a.UserID)
			case "token":
				token = "a0000000-0000-0000-0000-000000000022"
			case "sequence":
				sequence = 2
			case "job":
				l.Job.ID = "a0000000-0000-0000-0000-000000000022"
			}
			if err != nil {
				t.Fatal(err)
			}
			value, err := f.s.GetNFOWriteCommitFiles(f.ctx, l, sequence, token)
			if reason == "ready" || reason == "plan_only" {
				if err != nil || value.Record != record || !value.PlanRecorded || value.ReadyRecorded != (reason == "ready") || value.Plan != commitPlanFixture(p) {
					t.Fatal("read altered evidence", err)
				}
				if reason == "ready" && value.Ready != commitReadyFixture() {
					t.Fatal("ready read changed")
				}
				encoded, _ := json.Marshal(value)
				if string(encoded) != "{}" || strings.Contains(fmt.Sprintf("%+v %#v", value, value), token) {
					t.Fatal("private evidence exposed")
				}
			} else if err == nil || value != (domain.NFOWriteCommitFileEvidence{}) {
				t.Fatal("failed read returned partial evidence", err)
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if reason == "missing" {
				want = 0
			}
			if count != want {
				t.Fatal("read created journal")
			}
		})
	}
}

type unknownReadyStore struct {
	*Store
	reportLost bool
}

func (s *unknownReadyStore) SaveNFOWriteCommitFilesReady(ctx context.Context, l domain.JobLease, seq int, token string, ready domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	value, err := s.Store.SaveNFOWriteCommitFilesReady(ctx, l, seq, token, ready)
	if err == nil && s.reportLost {
		s.reportLost = false
		return domain.NFOWriteCommitFilesReady{}, domain.ErrDatabase
	}
	return value, err
}

func TestNFOCommitFileResumeUnknownReadyAndNativeChanges(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("native evidence unsupported")
	}
	for _, reason := range []string{"response_lost", "output_missing", "rollback_replaced", "pin_missing", "target_output", "parent_replaced", "cancel", "disabled", "expiry"} {
		t.Run(reason, func(t *testing.T) {
			f, l, p := nfoCommitFixture(t)
			record, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1)
			if err != nil {
				t.Fatal(err)
			}
			source, err := nfo.ReadSource(f.ctx, p.Scope.Source.RootPath, p.Scope.Source.RelativePath, p.Request.MaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
			writer, _ := nfo.NewWriterWithBudget(budget)
			unknown := &unknownReadyStore{Store: f.s, reportLost: true}
			if err := writer.StageCommitFiles(f.ctx, source, l, record, unknown); !errors.Is(err, nfo.ErrReplace) {
				t.Fatal("unknown committed response", err)
			}
			evidence, err := f.s.GetNFOWriteCommitFiles(f.ctx, l, 1, record.Token)
			if err != nil || !evidence.ReadyRecorded {
				t.Fatal("unknown outcome lost ready", err)
			}
			directory := filepath.Join(p.Scope.Source.RootPath, filepath.Dir(filepath.FromSlash(p.Scope.Source.RelativePath)))
			prefix := ".jelee-nfo-commit-" + strings.ReplaceAll(record.Token, "-", "") + "-"
			name := func(suffix string) string { return filepath.Join(directory, prefix+suffix) }
			switch reason {
			case "output_missing":
				err = os.Remove(name("output"))
			case "rollback_replaced":
				err = os.Remove(name("rollback"))
				if err == nil {
					err = os.WriteFile(name("rollback"), p.Original, 0600)
				}
			case "pin_missing":
				err = os.Remove(name("output-pin"))
			case "target_output":
				err = os.WriteFile(filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath)), p.Replacement, 0600)
			case "parent_replaced":
				err = os.Rename(directory, directory+"-old")
				if err == nil {
					err = os.Mkdir(directory, 0700)
				}
			case "cancel":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l.Job.ID)
			case "disabled":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID)
			case "expiry":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Compare complete remaining file contents before/after rejected resume.
			observedDirectory := directory
			if reason == "parent_replaced" {
				observedDirectory = directory + "-old"
			}
			before := commitResumeFiles(t, observedDirectory)
			err = writer.StageCommitFiles(f.ctx, source, l, record, f.s)
			if reason == "response_lost" {
				if err != nil {
					t.Fatal("ready resume failed", err)
				}
				after, err := f.s.GetNFOWriteCommitFiles(f.ctx, l, 1, record.Token)
				if err != nil || after != evidence {
					t.Fatal("resume changed immutable evidence", err)
				}
			} else if err == nil {
				t.Fatal("changed evidence or lease resumed")
			}
			after := commitResumeFiles(t, observedDirectory)
			if len(before) != len(after) {
				t.Fatal("resume changed retained files")
			}
			for name, data := range before {
				if !bytes.Equal(data, after[name]) {
					t.Fatal("resume modified file")
				}
			}
			if used, _ := budget.PayloadBytes(); used != 0 || budget.Stats() != (resources.Stats{}) {
				t.Fatal("resume leaked resources")
			}
		})
	}
}

func commitResumeFiles(t *testing.T, directory string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, entry := range entries {
		if !entry.IsDir() {
			data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[entry.Name()] = data
		}
	}
	return files
}
