//go:build windows || linux

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// settlementRepository mirrors the schema58 rules Settle relies on: only a
// ready namespace is settled, phases only move forward and keep one attempt.
// Hooks model a failed save (before) and a lost response (after).
type settlementRepository struct {
	*attemptStagingRepository
	smu        sync.Mutex
	phases     []domain.NFOWriteCommitSettlementPhase
	attempt    uint8
	beforeSave func(domain.NFOWriteCommitSettlementPhase) error
	afterSave  func(domain.NFOWriteCommitSettlementPhase) error
}

func (r *settlementRepository) selected() (bool, uint8, domain.NFOWriteCommitFilesReady) {
	r.retainedStagingRepository.mu.Lock()
	legacy := r.evidence
	r.retainedStagingRepository.mu.Unlock()
	if legacy.ReadyRecorded {
		return true, 0, legacy.Ready
	}
	attempts, _ := r.GetNFOWriteCommitAttempts(context.Background(), domain.JobLease{}, 1, "")
	for _, entry := range attempts.Attempts {
		if entry.Number != 0 && entry.ReadyRecorded {
			return true, entry.Number, entry.Ready
		}
	}
	return false, 0, domain.NFOWriteCommitFilesReady{}
}

// The journal keeps its original owner and generation under a recovery lease.
func (r *settlementRepository) GetNFOWriteCommitFiles(ctx context.Context, l domain.JobLease, seq int, token string) (domain.NFOWriteCommitFileEvidence, error) {
	value, err := r.retainedStagingRepository.GetNFOWriteCommitFiles(ctx, l, seq, token)
	_, record := stagingLease()
	value.Record.Owner = record.Owner
	return value, err
}

func (r *settlementRepository) GetNFOWriteCommitSettlement(context.Context, domain.JobLease, int, string) (domain.NFOWriteCommitSettlement, error) {
	ready, attempt, value := r.selected() //nolint:contextcheck // test fake reads fixed state; no I/O
	r.smu.Lock()
	defer r.smu.Unlock()
	result := domain.NFOWriteCommitSettlement{ReadyRecorded: ready, ReadyAttempt: attempt, Ready: value}
	for _, phase := range r.phases {
		result.Phase, result.Attempt = phase, r.attempt
	}
	return result, nil
}

func (r *settlementRepository) SaveNFOWriteCommitSettlement(ctx context.Context, l domain.JobLease, seq int, token string, attempt uint8, phase domain.NFOWriteCommitSettlementPhase) (domain.NFOWriteCommitSettlement, error) {
	if r.beforeSave != nil {
		if err := r.beforeSave(phase); err != nil {
			return domain.NFOWriteCommitSettlement{}, err
		}
	}
	ready, selected, _ := r.selected() //nolint:contextcheck // test fake reads fixed state; no I/O
	r.smu.Lock()
	if !ready || selected != attempt || len(r.phases) > 0 && r.attempt != attempt {
		r.smu.Unlock()
		return domain.NFOWriteCommitSettlement{}, domain.ErrInvalid
	}
	has := map[domain.NFOWriteCommitSettlementPhase]bool{}
	for _, p := range r.phases {
		has[p] = true
	}
	switch {
	case has[phase]:
	case phase == domain.NFOWriteCommitBackedUp && (has[domain.NFOWriteCommitReplaced] || has[domain.NFOWriteCommitRolledBack]),
		phase == domain.NFOWriteCommitReplaced && (!has[domain.NFOWriteCommitBackedUp] || has[domain.NFOWriteCommitRolledBack]),
		phase == domain.NFOWriteCommitRolledBack && has[domain.NFOWriteCommitReplaced]:
		r.smu.Unlock()
		return domain.NFOWriteCommitSettlement{}, domain.ErrInvalid
	default:
		r.phases = append(r.phases, phase)
		r.attempt = attempt
	}
	r.smu.Unlock()
	if r.afterSave != nil {
		if err := r.afterSave(phase); err != nil {
			return domain.NFOWriteCommitSettlement{}, err
		}
	}
	return r.GetNFOWriteCommitSettlement(ctx, l, seq, token)
}

func (r *settlementRepository) recorded() []domain.NFOWriteCommitSettlementPhase {
	r.smu.Lock()
	defer r.smu.Unlock()
	return append([]domain.NFOWriteCommitSettlementPhase(nil), r.phases...)
}

type settleWriterFixture struct {
	root, relative string
	prepared       domain.NFOWritePreparation
	budget         *resources.Budget
	repo           *settlementRepository
	lease          domain.JobLease
	record         domain.NFOWriteCommitRecord
}

func newSettleWriterFixture(t *testing.T) settleWriterFixture {
	t.Helper()
	root, source, p, b := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
	l, record := stagingLease()
	repo := &settlementRepository{attemptStagingRepository: &attemptStagingRepository{retainedStagingRepository: &retainedStagingRepository{stagingRepository: &stagingRepository{read: func(_ context.Context, l domain.JobLease, seq int) (domain.NFOWriteTask, error) {
		return domain.NFOWriteTask{JobID: l.Job.ID, Sequence: seq, Preparation: p}, nil
	}}}}}
	// Two retained backups exist before the write; rotation must keep them.
	for i, data := range []string{"older backup 0", "older backup 1"} {
		if err := os.WriteFile(filepath.Join(root, nfoBackupName(source.relative, i)), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return settleWriterFixture{root: root, relative: source.relative, prepared: p, budget: b, repo: repo, lease: l, record: record}
}

func (f settleWriterFixture) source(t *testing.T) *Source {
	t.Helper()
	source, err := ReadSource(context.Background(), f.root, f.relative, f.prepared.Request.MaxBytes)
	if err != nil {
		t.Fatal("fresh settle source unavailable")
	}
	return source
}

func (f settleWriterFixture) stage(t *testing.T) {
	t.Helper()
	w, _ := NewWriterWithBudget(f.budget)
	if err := w.StageCommitFiles(context.Background(), f.source(t), f.lease, f.record, f.repo); err != nil {
		t.Fatal("stage before settle", err)
	}
}

func (f settleWriterFixture) settle(t *testing.T, ops nfoWriteOperations) error {
	t.Helper()
	w, _ := NewWriterWithBudget(f.budget)
	err := w.settleCommitFiles(context.Background(), f.source(t), f.lease, f.record, f.repo, ops)
	if used, _ := f.budget.PayloadBytes(); used != 0 {
		t.Fatal("settle leaked payload bytes")
	}
	return err
}

func (f settleWriterFixture) target(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, f.relative))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (f settleWriterFixture) backup(t *testing.T, index int) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, nfoBackupName(f.relative, index)))
	if err != nil {
		t.Fatal("retained backup missing", index)
	}
	return data
}

func phasesEqual(got []domain.NFOWriteCommitSettlementPhase, want ...domain.NFOWriteCommitSettlementPhase) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestSettleCommitFilesReplacesWithBackup(t *testing.T) {
	f := newSettleWriterFixture(t)
	f.stage(t)
	if err := f.settle(t, nativeNFOWriteOperations()); err != nil {
		t.Fatal("settle failed", err)
	}
	if !bytes.Equal(f.target(t), f.prepared.Replacement) {
		t.Fatal("target not replaced")
	}
	// The newest backup is the original; the series shifted, and the evicted
	// backup stays under a settlement-owned name.
	if !bytes.Equal(f.backup(t, 0), f.prepared.Original) || string(f.backup(t, 1)) != "older backup 0" {
		t.Fatal("backup rotation differs")
	}
	if _, err := os.Stat(filepath.Join(f.root, nfoBackupName(f.relative, 2))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("backup retained beyond the requested count")
	}
	if !phasesEqual(f.repo.recorded(), domain.NFOWriteCommitBackedUp, domain.NFOWriteCommitReplaced) {
		t.Fatal("settlement phases differ")
	}
	// Terminal replay neither touches files nor records anything new.
	if err := os.WriteFile(filepath.Join(f.root, f.relative), []byte(`<movie><title>user</title></movie>`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.settle(t, nativeNFOWriteOperations()); err != nil || string(f.target(t)) != `<movie><title>user</title></movie>` {
		t.Fatal("terminal replay changed the target", err)
	}
}

func TestSettleCommitFilesRollsBackAfterRenameFailure(t *testing.T) {
	f := newSettleWriterFixture(t)
	f.stage(t)
	ops := nativeNFOWriteOperations()
	syncs := 0
	ops.syncDirectory = func(r *os.Root) error {
		syncs++
		// Rotation syncs first; the sync after the target Rename fails.
		if syncs == 2 {
			return errors.New("owned directory sync failure")
		}
		return syncNFODirectory(r)
	}
	if err := f.settle(t, ops); !errors.Is(err, ErrRolledBack) {
		t.Fatal("failed commit not rolled back", err)
	}
	if !bytes.Equal(f.target(t), f.prepared.Original) {
		t.Fatal("rollback did not restore the original bytes")
	}
	if !phasesEqual(f.repo.recorded(), domain.NFOWriteCommitBackedUp, domain.NFOWriteCommitRolledBack) {
		t.Fatal("rollback phases differ")
	}
	if err := f.settle(t, nativeNFOWriteOperations()); !errors.Is(err, ErrRolledBack) || !bytes.Equal(f.target(t), f.prepared.Original) {
		t.Fatal("rolled back token replaced on replay", err)
	}
}

func TestSettleCommitFilesResumesEveryPhase(t *testing.T) {
	lost := errors.New("owned lost response")
	for _, test := range []struct {
		name        string
		configure   func(*settlementRepository)
		first       error
		afterTarget func(settleWriterFixture) []byte
	}{
		// BackedUp committed but its response was lost: nothing was renamed and
		// the rotation was undone; the retry rotates again and replays the phase.
		{"backed-up-lost", func(r *settlementRepository) {
			once := false
			r.afterSave = func(p domain.NFOWriteCommitSettlementPhase) error {
				if p == domain.NFOWriteCommitBackedUp && !once {
					once = true
					return lost
				}
				return nil
			}
		}, ErrReplace, func(f settleWriterFixture) []byte { return f.prepared.Original }},
		// Renamed but the replaced phase never committed.
		{"replaced-unsaved", func(r *settlementRepository) {
			once := false
			r.beforeSave = func(p domain.NFOWriteCommitSettlementPhase) error {
				if p == domain.NFOWriteCommitReplaced && !once {
					once = true
					return lost
				}
				return nil
			}
		}, ErrReplace, func(f settleWriterFixture) []byte { return f.prepared.Replacement }},
		// Replaced committed but its response was lost.
		{"replaced-lost", func(r *settlementRepository) {
			once := false
			r.afterSave = func(p domain.NFOWriteCommitSettlementPhase) error {
				if p == domain.NFOWriteCommitReplaced && !once {
					once = true
					return lost
				}
				return nil
			}
		}, ErrReplace, func(f settleWriterFixture) []byte { return f.prepared.Replacement }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSettleWriterFixture(t)
			f.stage(t)
			test.configure(f.repo)
			if err := f.settle(t, nativeNFOWriteOperations()); !errors.Is(err, test.first) {
				t.Fatal("interrupted settle result", err)
			}
			if !bytes.Equal(f.target(t), test.afterTarget(f)) {
				t.Fatal("interrupted target differs")
			}
			// A fresh source observes the possibly renamed target.
			if err := f.settle(t, nativeNFOWriteOperations()); err != nil {
				t.Fatal("resume failed", err)
			}
			if !bytes.Equal(f.target(t), f.prepared.Replacement) || !bytes.Equal(f.backup(t, 0), f.prepared.Original) || string(f.backup(t, 1)) != "older backup 0" {
				t.Fatal("resumed files differ")
			}
			if !phasesEqual(f.repo.recorded(), domain.NFOWriteCommitBackedUp, domain.NFOWriteCommitReplaced) {
				t.Fatal("resumed phases differ")
			}
		})
	}
}

func TestSettleCommitFilesRecoveryLeaseResumes(t *testing.T) {
	f := newSettleWriterFixture(t)
	f.stage(t)
	f.repo.beforeSave = func(p domain.NFOWriteCommitSettlementPhase) error {
		if p == domain.NFOWriteCommitReplaced {
			return domain.ErrJobLeaseLost
		}
		return nil
	}
	if err := f.settle(t, nativeNFOWriteOperations()); !errors.Is(err, ErrReplace) {
		t.Fatal("lost original lease not reported", err)
	}
	f.repo.beforeSave = nil
	// A recovery holder continues the original token without owning it.
	f.lease.Owner, f.lease.RecoveryEpoch = "recovery-owner", 1
	if err := f.settle(t, nativeNFOWriteOperations()); err != nil || !bytes.Equal(f.target(t), f.prepared.Replacement) {
		t.Fatal("recovery lease did not finish the replacement", err)
	}
}

func TestSettleCommitFilesCancelledOnlyRollsBack(t *testing.T) {
	for _, renamed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-rename", true: "after-rename"}[renamed], func(t *testing.T) {
			f := newSettleWriterFixture(t)
			f.stage(t)
			if renamed {
				f.repo.beforeSave = func(p domain.NFOWriteCommitSettlementPhase) error {
					if p == domain.NFOWriteCommitReplaced {
						return context.Canceled
					}
					return nil
				}
				if err := f.settle(t, nativeNFOWriteOperations()); err == nil || !bytes.Equal(f.target(t), f.prepared.Replacement) {
					t.Fatal("cancelled replacement fixture differs", err)
				}
				f.repo.beforeSave = nil
			}
			f.lease.Owner, f.lease.RecoveryEpoch, f.lease.Job.CancelRequested = "recovery-owner", 1, true
			if err := f.settle(t, nativeNFOWriteOperations()); !errors.Is(err, ErrRolledBack) {
				t.Fatal("cancelled token not rolled back", err)
			}
			if !bytes.Equal(f.target(t), f.prepared.Original) {
				t.Fatal("cancelled write left the replacement")
			}
			want := []domain.NFOWriteCommitSettlementPhase{domain.NFOWriteCommitRolledBack}
			if renamed {
				want = []domain.NFOWriteCommitSettlementPhase{domain.NFOWriteCommitBackedUp, domain.NFOWriteCommitRolledBack}
			}
			if !phasesEqual(f.repo.recorded(), want...) {
				t.Fatal("cancelled phases differ")
			}
		})
	}
}

func TestSettleCommitFilesRefusesForeignTarget(t *testing.T) {
	f := newSettleWriterFixture(t)
	f.stage(t)
	foreign := []byte(`<movie><title>edited by user</title></movie>`)
	if err := os.Remove(filepath.Join(f.root, f.relative)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, f.relative), foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.settle(t, nativeNFOWriteOperations()); !errors.Is(err, ErrChanged) {
		t.Fatal("foreign target settled", err)
	}
	if !bytes.Equal(f.target(t), foreign) || len(f.repo.recorded()) != 0 {
		t.Fatal("foreign target changed or phase recorded")
	}
	w, _ := NewWriterWithBudget(f.budget)
	if err := w.CommitNFOWriteEntry(context.Background(), f.lease, f.record, f.repo); !errors.Is(err, domain.ErrNFOWriteRejected) {
		t.Fatal("foreign target not classified as rejected", err)
	}
	// Abort concludes the token without touching the foreign object.
	if phase, err := w.AbortNFOWriteEntry(context.Background(), f.lease, f.record, f.repo); err != nil || phase != domain.NFOWriteCommitRolledBack {
		t.Fatal("abort did not conclude the token", err)
	}
	if !bytes.Equal(f.target(t), foreign) {
		t.Fatal("abort replaced a foreign object")
	}
}

func TestCommitNFOWriteEntryStagesAndSettles(t *testing.T) {
	f := newSettleWriterFixture(t)
	w, _ := NewWriterWithBudget(f.budget)
	if err := w.CommitNFOWriteEntry(context.Background(), f.lease, f.record, f.repo); err != nil {
		t.Fatal("entry commit failed", err)
	}
	if !bytes.Equal(f.target(t), f.prepared.Replacement) {
		t.Fatal("entry commit did not replace the target")
	}
	if err := w.CommitNFOWriteEntry(context.Background(), f.lease, f.record, f.repo); err != nil {
		t.Fatal("entry replay failed", err)
	}
	// A token without ready evidence has nothing to abort.
	g := newSettleWriterFixture(t)
	if phase, err := w.AbortNFOWriteEntry(context.Background(), g.lease, g.record, g.repo); err != nil || phase != 0 {
		t.Fatal("unready abort", phase, err)
	}
}
