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

// attemptStagingRepository mirrors the schema56 rules that Stage relies on:
// first values are immutable, attempts are allocated in order, and legacy
// evidence excludes a new namespace.
type attemptStagingRepository struct {
	*retainedStagingRepository
	mu          sync.Mutex
	attempts    domain.NFOWriteCommitAttempts
	allocations int
	lostReady   bool
}

func (r *attemptStagingRepository) SaveNFOWriteCommitFileCheckpoint(_ context.Context, _ domain.JobLease, _ int, _ string, v domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	r.retainedStagingRepository.mu.Lock()
	defer r.retainedStagingRepository.mu.Unlock()
	if r.latest() != 0 {
		return domain.NFOWriteCommitFileCheckpoint{}, domain.ErrConflict
	}
	r.evidence.CheckpointRecorded, r.evidence.Checkpoint = true, v
	return v, nil
}
func (r *attemptStagingRepository) SaveNFOWriteCommitFilesReady(ctx context.Context, l domain.JobLease, seq int, token string, v domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	if r.latest() != 0 {
		return domain.NFOWriteCommitFilesReady{}, domain.ErrConflict
	}
	return r.retainedStagingRepository.SaveNFOWriteCommitFilesReady(ctx, l, seq, token, v)
}
func (r *attemptStagingRepository) latest() uint8 {
	r.mu.Lock()
	defer r.mu.Unlock()
	latest := uint8(0)
	for _, entry := range r.attempts.Attempts {
		if entry.Number != 0 {
			latest = entry.Number
		}
	}
	return latest
}
func (r *attemptStagingRepository) ReserveNFOWriteCommitAttempts(_ context.Context, _ domain.JobLease, _ int, _ string, v domain.NFOWriteCommitAttemptReservation) (domain.NFOWriteCommitAttemptReservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.attempts.ReservationRecorded {
		r.attempts.ReservationRecorded, r.attempts.Reservation = true, v
	}
	return r.attempts.Reservation, nil
}
func (r *attemptStagingRepository) AllocateNFOWriteCommitAttempt(_ context.Context, _ domain.JobLease, _ int, _ string, after uint8) (uint8, error) {
	r.retainedStagingRepository.mu.Lock()
	legacyReady := r.evidence.ReadyRecorded
	r.retainedStagingRepository.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	latest := uint8(0)
	for _, entry := range r.attempts.Attempts {
		if entry.Number != 0 {
			latest = entry.Number
		}
	}
	if legacyReady || after >= domain.NFOWriteCommitAttemptLimit || latest < after {
		return 0, domain.ErrConflict
	}
	r.allocations++
	if r.attempts.Attempts[after].Number == 0 {
		r.attempts.Attempts[after].Number = after + 1
	}
	return after + 1, nil
}
func (r *attemptStagingRepository) GetNFOWriteCommitAttempts(context.Context, domain.JobLease, int, string) (domain.NFOWriteCommitAttempts, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts, nil
}
func (r *attemptStagingRepository) SaveNFOWriteCommitAttemptCheckpoint(_ context.Context, _ domain.JobLease, _ int, _ string, attempt uint8, v domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := &r.attempts.Attempts[attempt-1]
	if entry.Number != attempt {
		return domain.NFOWriteCommitFileCheckpoint{}, domain.ErrConflict
	}
	if entry.CheckpointRecorded && (entry.Checkpoint.OutputIdentity != v.OutputIdentity || entry.Checkpoint.Phase > v.Phase) {
		return domain.NFOWriteCommitFileCheckpoint{}, domain.ErrConflict
	}
	entry.CheckpointRecorded, entry.Checkpoint = true, v
	return v, nil
}
func (r *attemptStagingRepository) SaveNFOWriteCommitAttemptReady(_ context.Context, _ domain.JobLease, _ int, _ string, attempt uint8, v domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := &r.attempts.Attempts[attempt-1]
	if entry.Number != attempt || entry.ReadyRecorded && entry.Ready != v {
		return domain.NFOWriteCommitFilesReady{}, domain.ErrConflict
	}
	entry.ReadyRecorded, entry.Ready = true, v
	if r.lostReady {
		r.lostReady = false
		return domain.NFOWriteCommitFilesReady{}, domain.ErrDatabase
	}
	return v, nil
}

func attemptStagingFixture(t *testing.T) (string, *Source, domain.NFOWritePreparation, *attemptStagingRepository, func() error) {
	t.Helper()
	root, source, p, b := preparedWriterFixture(t, []byte(`<movie><title>old</title></movie>`), "Movie")
	l, record := stagingLease()
	repo := &attemptStagingRepository{retainedStagingRepository: &retainedStagingRepository{stagingRepository: &stagingRepository{read: func(_ context.Context, l domain.JobLease, seq int) (domain.NFOWriteTask, error) {
		return domain.NFOWriteTask{JobID: l.Job.ID, Sequence: seq, Preparation: p}, nil
	}}}}
	stage := func() error {
		fresh, err := ReadSource(context.Background(), root, source.relative, p.Request.MaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		w, _ := NewWriterWithBudget(b)
		err = w.StageCommitFiles(context.Background(), fresh, l, record, repo)
		if used, _ := b.PayloadBytes(); used != 0 {
			t.Fatal("attempt stage leaked bytes")
		}
		return err
	}
	return root, source, p, repo, stage
}
func attemptStagingName(root string, attempt uint8, suffix string) string {
	_, record := stagingLease()
	base := ".jelee-nfo-commit-" + strings.ReplaceAll(record.Token, "-", "")
	if attempt != 0 {
		base += "-attempt-" + string(rune('0'+attempt))
	}
	return filepath.Join(root, base+"-"+suffix)
}
func attemptStagingUnknown(t *testing.T, root string, attempt uint8) string {
	t.Helper()
	name := attemptStagingName(root, attempt, "output")
	if err := os.WriteFile(name, []byte("unknown interrupted object"), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}
func attemptStagingRetained(t *testing.T, name string) {
	t.Helper()
	if data, err := os.ReadFile(name); err != nil || string(data) != "unknown interrupted object" {
		t.Fatal("unknown object was adopted, changed or removed")
	}
}
func attemptStagingRecordPlan(t *testing.T, repo *attemptStagingRepository, stage func() error) {
	t.Helper()
	// An unknown ready response persists the plan and leaves legacy names, which
	// the test then clears to model an interrupted create before any checkpoint.
	repo.lost = true
	if err := stage(); !errors.Is(err, ErrReplace) {
		t.Fatal("legacy unknown response", err)
	}
	repo.retainedStagingRepository.mu.Lock()
	repo.evidence.ReadyRecorded, repo.evidence.Ready = false, domain.NFOWriteCommitFilesReady{}
	repo.evidence.CheckpointRecorded, repo.evidence.Checkpoint = false, domain.NFOWriteCommitFileCheckpoint{}
	repo.retainedStagingRepository.mu.Unlock()
}
func attemptStagingClear(t *testing.T, root string, attempt uint8) {
	t.Helper()
	for _, suffix := range []string{"original-pin", "output", "output-pin", "rollback", "rollback-pin"} {
		if err := os.Remove(attemptStagingName(root, attempt, suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

func TestStageCommitAttemptsFreshUsesLegacyNamespace(t *testing.T) {
	root, _, _, repo, stage := attemptStagingFixture(t)
	if err := stage(); err != nil {
		t.Fatal("fresh stage failed", err)
	}
	if !repo.evidence.ReadyRecorded || repo.allocations != 0 || repo.latest() != 0 {
		t.Fatal("fresh stage did not use the legacy namespace")
	}
	if _, err := os.Lstat(attemptStagingName(root, 0, "output")); err != nil {
		t.Fatal("legacy output missing")
	}
}

func TestStageCommitAttemptsRotatesPastUnknownNames(t *testing.T) {
	root, _, _, repo, stage := attemptStagingFixture(t)
	attemptStagingRecordPlan(t, repo, stage)
	attemptStagingClear(t, root, 0)
	unknown := attemptStagingUnknown(t, root, 0)
	if err := stage(); err != nil {
		t.Fatal("rotation failed", err)
	}
	attemptStagingRetained(t, unknown)
	if repo.latest() != 1 || repo.allocations != 1 || !repo.attempts.Attempts[0].ReadyRecorded || !repo.attempts.ReservationRecorded || repo.evidence.ReadyRecorded {
		t.Fatal("attempt one not selected")
	}
	if _, err := os.Lstat(attemptStagingName(root, 1, "rollback-pin")); err != nil {
		t.Fatal("attempt one names missing")
	}
	// A fresh replay verifies the selected attempt; it never allocates again.
	if err := stage(); err != nil || repo.allocations != 1 {
		t.Fatal("ready attempt replay failed or reallocated", err)
	}
	attemptStagingRetained(t, unknown)
}

func TestStageCommitAttemptsRetriesCleanNamespace(t *testing.T) {
	root, _, _, repo, stage := attemptStagingFixture(t)
	attemptStagingRecordPlan(t, repo, stage)
	attemptStagingClear(t, root, 0)
	if err := stage(); err != nil {
		t.Fatal("clean legacy retry failed", err)
	}
	if repo.allocations != 0 || !repo.evidence.ReadyRecorded {
		t.Fatal("clean namespace rotated needlessly")
	}
}

func TestStageCommitAttemptsBoundedExhaustion(t *testing.T) {
	root, _, _, repo, stage := attemptStagingFixture(t)
	attemptStagingRecordPlan(t, repo, stage)
	attemptStagingClear(t, root, 0)
	var unknown []string
	unknown = append(unknown, attemptStagingUnknown(t, root, 0))
	for attempt := uint8(1); attempt <= 3; attempt++ {
		repo.attempts.ReservationRecorded = true
		repo.attempts.Attempts[attempt-1].Number = attempt
		unknown = append(unknown, attemptStagingUnknown(t, root, attempt))
	}
	if err := stage(); !errors.Is(err, ErrCommitAttemptsExhausted) {
		t.Fatal("exhaustion not bounded", err)
	}
	if repo.allocations != 0 {
		t.Fatal("exhausted stage allocated")
	}
	for _, name := range unknown {
		attemptStagingRetained(t, name)
	}
}

func TestStageCommitAttemptsSavedCheckpointIsPinned(t *testing.T) {
	for _, reason := range []string{"resume", "output_changed"} {
		t.Run(reason, func(t *testing.T) {
			root, _, _, repo, stage := attemptStagingFixture(t)
			attemptStagingRecordPlan(t, repo, stage)
			attemptStagingClear(t, root, 0)
			attemptStagingUnknown(t, root, 0)
			// Lose the attempt ready response: attempt one keeps its saved checkpoints.
			repo.lostReady = true
			if err := stage(); !errors.Is(err, ErrReplace) {
				t.Fatal("unknown attempt ready response", err)
			}
			entry := &repo.attempts.Attempts[0]
			entry.ReadyRecorded, entry.Ready = false, domain.NFOWriteCommitFilesReady{}
			if !entry.CheckpointRecorded || entry.Checkpoint.Phase != 2 {
				t.Fatal("attempt checkpoint missing")
			}
			if reason == "output_changed" {
				name := attemptStagingName(root, 1, "output")
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, []byte("<movie><title>other</title></movie>"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := stage()
			if reason == "resume" {
				if err != nil || !repo.attempts.Attempts[0].ReadyRecorded {
					t.Fatal("saved checkpoint did not resume", err)
				}
			} else if err == nil || errors.Is(err, ErrCommitAttemptsExhausted) {
				t.Fatal("changed first output accepted or rotated", err)
			}
			if repo.allocations != 1 || repo.latest() != 1 {
				t.Fatal("saved checkpoint rotated to another namespace")
			}
		})
	}
}

func TestStageCommitAttemptsLegacyExclusive(t *testing.T) {
	_, _, _, repo, stage := attemptStagingFixture(t)
	if err := stage(); err != nil {
		t.Fatal(err)
	}
	repo.attempts.ReservationRecorded = true
	repo.attempts.Attempts[0].Number = 1
	if err := stage(); !errors.Is(err, ErrChanged) {
		t.Fatal("legacy ready accepted beside an allocated attempt", err)
	}
}
