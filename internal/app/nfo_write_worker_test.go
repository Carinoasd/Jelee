package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// nfoWriteExecutionFake embeds the interface: an unexpected call panics.
type nfoWriteExecutionFake struct {
	NFOWriteExecutionRepository
	mu        sync.Mutex
	entries   []domain.NFOWriteCommitEntryState
	recovery  domain.JobLease
	completed int
	renewErr  error
	renewals  int
}

func (f *nfoWriteExecutionFake) ListNFOWriteCommitEntries(context.Context, domain.JobLease) ([]domain.NFOWriteCommitEntryState, error) {
	return f.entries, nil
}
func (f *nfoWriteExecutionFake) BeginNFOWriteCommit(_ context.Context, l domain.JobLease, seq int) (domain.NFOWriteCommitRecord, error) {
	return domain.NFOWriteCommitRecord{JobID: l.Job.ID, Sequence: seq, Owner: l.Owner, Generation: l.Generation, Token: "c0000000-0000-4000-8000-00000000000" + string(rune('0'+seq))}, nil
}
func (f *nfoWriteExecutionFake) GetNFOWriteCommitFiles(_ context.Context, l domain.JobLease, seq int, token string) (domain.NFOWriteCommitFileEvidence, error) {
	return domain.NFOWriteCommitFileEvidence{Record: domain.NFOWriteCommitRecord{JobID: l.Job.ID, Sequence: seq, Owner: "original-owner", Generation: l.Generation, Token: token}}, nil
}
func (f *nfoWriteExecutionFake) ListNFOWriteCommitRecoveries(context.Context, int) ([]string, error) {
	return []string{f.recovery.Job.ID}, nil
}
func (f *nfoWriteExecutionFake) AcquireNFOWriteCommitRecovery(context.Context, string, string, time.Duration) (domain.JobLease, error) {
	return f.recovery, nil
}
func (f *nfoWriteExecutionFake) RenewNFOWriteCommitRecovery(context.Context, domain.JobLease, time.Duration) (domain.JobLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewals++
	return f.recovery, f.renewErr
}
func (f *nfoWriteExecutionFake) CompleteNFOWriteCommitRecovery(context.Context, domain.JobLease) (string, error) {
	f.completed++
	return f.recovery.Job.State, nil
}

type nfoWriteCommitterFake struct {
	commit  func(domain.NFOWriteCommitRecord) error
	commits []int
	aborts  []int
}

func (f *nfoWriteCommitterFake) CommitNFOWriteEntry(_ context.Context, _ domain.JobLease, r domain.NFOWriteCommitRecord, _ NFOWriteCommitSettlementRepository) error {
	f.commits = append(f.commits, r.Sequence)
	return f.commit(r)
}
func (f *nfoWriteCommitterFake) AbortNFOWriteEntry(_ context.Context, _ domain.JobLease, r domain.NFOWriteCommitRecord, _ NFOWriteCommitSettlementRepository) (domain.NFOWriteCommitSettlementPhase, error) {
	f.aborts = append(f.aborts, r.Sequence)
	return domain.NFOWriteCommitRolledBack, nil
}

const nfoWriteWorkerOwner = "c0000000-0000-4000-8000-000000000100"

func nfoWriteRunLease() domain.JobLease {
	return domain.JobLease{Job: domain.Job{ID: "c0000000-0000-4000-8000-000000000200", Kind: domain.JobNFOWrite}, Owner: nfoWriteWorkerOwner, Generation: 1}
}

func TestNFOWriteWorkerValidatesConstruction(t *testing.T) {
	repository, committer := &nfoWriteExecutionFake{}, &nfoWriteCommitterFake{}
	for _, test := range []struct {
		repository NFOWriteExecutionRepository
		committer  NFOWriteCommitter
		owner      string
		lease      time.Duration
	}{{nil, committer, nfoWriteWorkerOwner, time.Minute}, {repository, nil, nfoWriteWorkerOwner, time.Minute}, {repository, committer, "not-an-id", time.Minute}, {repository, committer, nfoWriteWorkerOwner, time.Second}, {repository, committer, nfoWriteWorkerOwner, 2 * time.Hour}} {
		if _, err := NewNFOWriteWorker(test.repository, test.committer, test.owner, test.lease); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("invalid worker accepted")
		}
	}
	w, _ := NewNFOWriteWorker(repository, committer, nfoWriteWorkerOwner, time.Minute)
	recovery := nfoWriteRunLease()
	recovery.RecoveryEpoch = 1
	if err := w.Run(context.Background(), recovery); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("recovery lease ran new entries")
	}
}

func TestNFOWriteWorkerRunContinuesAfterRejection(t *testing.T) {
	repository := &nfoWriteExecutionFake{entries: []domain.NFOWriteCommitEntryState{{Sequence: 1}, {Sequence: 2, Token: "c0000000-0000-4000-8000-000000000002", Settlement: domain.NFOWriteCommitReplaced}, {Sequence: 3}}}
	committer := &nfoWriteCommitterFake{commit: func(r domain.NFOWriteCommitRecord) error {
		if r.Sequence == 1 {
			return errors.Join(domain.ErrNFOWriteRejected, errors.New("owned changed target"))
		}
		return nil
	}}
	w, _ := NewNFOWriteWorker(repository, committer, nfoWriteWorkerOwner, time.Minute)
	if err := w.Run(context.Background(), nfoWriteRunLease()); !errors.Is(err, ErrNFOWritePartial) {
		t.Fatal("rejected entry not reported", err)
	}
	// The replaced entry is skipped; the rejected one is aborted and the batch continues.
	if len(committer.commits) != 2 || committer.commits[0] != 1 || committer.commits[1] != 3 || len(committer.aborts) != 1 || committer.aborts[0] != 1 {
		t.Fatal("run order differs", committer.commits, committer.aborts)
	}
	stop := &nfoWriteCommitterFake{commit: func(domain.NFOWriteCommitRecord) error { return domain.ErrDatabase }}
	w, _ = NewNFOWriteWorker(repository, stop, nfoWriteWorkerOwner, time.Minute)
	if err := w.Run(context.Background(), nfoWriteRunLease()); !errors.Is(err, domain.ErrDatabase) || len(stop.commits) != 1 || len(stop.aborts) != 0 {
		t.Fatal("infrastructure failure did not stop the run for recovery", err)
	}
}

func TestNFOWriteWorkerRecoverCancelledOnlyAborts(t *testing.T) {
	recovery := nfoWriteRunLease()
	recovery.Owner, recovery.RecoveryEpoch, recovery.Job.State, recovery.Job.CancelRequested = nfoWriteWorkerOwner, 1, domain.JobCancelled, true
	repository := &nfoWriteExecutionFake{recovery: recovery, entries: []domain.NFOWriteCommitEntryState{
		{Sequence: 1, Token: "c0000000-0000-4000-8000-000000000001", Settlement: domain.NFOWriteCommitBackedUp},
		{Sequence: 2},
		{Sequence: 3, Token: "c0000000-0000-4000-8000-000000000003", Settlement: domain.NFOWriteCommitRolledBack},
	}}
	committer := &nfoWriteCommitterFake{commit: func(domain.NFOWriteCommitRecord) error { return nil }}
	w, _ := NewNFOWriteWorker(repository, committer, nfoWriteWorkerOwner, time.Minute)
	resolved, err := w.RecoverPending(context.Background(), 4)
	if err != nil || resolved != 1 || repository.completed != 1 {
		t.Fatal("cancelled recovery not resolved", err)
	}
	if len(committer.commits) != 0 || len(committer.aborts) != 1 || committer.aborts[0] != 1 {
		t.Fatal("cancelled recovery advanced a write", committer.commits, committer.aborts)
	}
}

func TestNFOWriteWorkerRecoverRetriesOrConcludes(t *testing.T) {
	recovery := nfoWriteRunLease()
	recovery.RecoveryEpoch, recovery.Job.State = 1, domain.JobFailed
	entries := []domain.NFOWriteCommitEntryState{{Sequence: 1, Token: "c0000000-0000-4000-8000-000000000001", ReadyRecorded: true}}
	for _, test := range []struct {
		err       error
		retry     bool
		completed int
	}{{nil, false, 1}, {domain.ErrDatabase, true, 0}, {domain.ErrJobLeaseLost, true, 0}, {errors.New("owned indeterminate stage"), false, 1}} {
		repository := &nfoWriteExecutionFake{recovery: recovery, entries: entries}
		committer := &nfoWriteCommitterFake{commit: func(domain.NFOWriteCommitRecord) error { return test.err }}
		w, _ := NewNFOWriteWorker(repository, committer, nfoWriteWorkerOwner, time.Minute)
		_, err := w.Recover(context.Background(), recovery.Job.ID)
		if (err != nil) != test.retry || repository.completed != test.completed {
			t.Fatal("recovery outcome differs", test.err, err)
		}
		if aborted := len(committer.aborts) == 1; aborted != (test.err != nil && !test.retry) {
			t.Fatal("recovery abort differs", test.err)
		}
	}
}

func TestNFOWriteWorkerRecoverStopsWhenRenewalFails(t *testing.T) {
	recovery := nfoWriteRunLease()
	recovery.RecoveryEpoch, recovery.Job.State = 1, domain.JobFailed
	repository := &nfoWriteExecutionFake{recovery: recovery, renewErr: domain.ErrJobLeaseLost, entries: []domain.NFOWriteCommitEntryState{{Sequence: 1, Token: "c0000000-0000-4000-8000-000000000001", ReadyRecorded: true}}}
	committer := &nfoWriteCommitterFake{}
	committer.commit = func(domain.NFOWriteCommitRecord) error {
		// Hold the entry until the renewal at a third of the lease fails.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			repository.mu.Lock()
			renewed := repository.renewals
			repository.mu.Unlock()
			if renewed > 0 {
				return context.Canceled
			}
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	}
	w, _ := NewNFOWriteWorker(repository, committer, nfoWriteWorkerOwner, 3*time.Second)
	if _, err := w.Recover(context.Background(), recovery.Job.ID); !errors.Is(err, domain.ErrJobLeaseLost) || repository.completed != 0 {
		t.Fatal("lost recovery lease did not stop the work", err)
	}
}
