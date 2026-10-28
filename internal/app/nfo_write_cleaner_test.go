package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	cleanupOwner  = "c1000000-0000-4000-8000-000000000001"
	cleanupTokenA = "c2000000-0000-4000-8000-00000000000a"
	cleanupTokenB = "c2000000-0000-4000-8000-00000000000b"
)

type nfoWriteCleanupFake struct {
	mu         sync.Mutex
	tokens     []string
	acquireErr map[string]error
	renewErr   error
	renewals   int
	completed  map[string]domain.NFOWriteCommitCleanupOutcome
}

func (f *nfoWriteCleanupFake) ListNFOWriteCommitCleanups(context.Context, int) ([]string, error) {
	return f.tokens, nil
}
func (f *nfoWriteCleanupFake) AcquireNFOWriteCommitCleanup(_ context.Context, token, owner string, _ time.Duration) (domain.NFOWriteCommitCleanupLease, error) {
	if err := f.acquireErr[token]; err != nil {
		return domain.NFOWriteCommitCleanupLease{}, err
	}
	return domain.NFOWriteCommitCleanupLease{Token: token, Owner: owner, Epoch: 1}, nil
}
func (f *nfoWriteCleanupFake) RenewNFOWriteCommitCleanup(_ context.Context, l domain.NFOWriteCommitCleanupLease, _ time.Duration) (domain.NFOWriteCommitCleanupLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewals++
	return l, f.renewErr
}
func (f *nfoWriteCleanupFake) GetNFOWriteCommitCleanupInput(_ context.Context, l domain.NFOWriteCommitCleanupLease) (domain.NFOWriteCommitCleanupInput, error) {
	return domain.NFOWriteCommitCleanupInput{Lease: l}, nil
}
func (f *nfoWriteCleanupFake) CompleteNFOWriteCommitCleanup(_ context.Context, l domain.NFOWriteCommitCleanupLease, outcome domain.NFOWriteCommitCleanupOutcome, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.completed == nil {
		f.completed = map[string]domain.NFOWriteCommitCleanupOutcome{}
	}
	f.completed[l.Token] = outcome
	return nil
}

type nfoWriteFileCleanerFake func(context.Context, domain.NFOWriteCommitCleanupInput) (domain.NFOWriteCommitCleanupOutcome, int, error)

func (f nfoWriteFileCleanerFake) CleanNFOWriteCommitFiles(ctx context.Context, input domain.NFOWriteCommitCleanupInput) (domain.NFOWriteCommitCleanupOutcome, int, error) {
	return f(ctx, input)
}

func TestNFOWriteCleanerCleansPendingAndSkipsHeldTokens(t *testing.T) {
	repo := &nfoWriteCleanupFake{tokens: []string{cleanupTokenA, cleanupTokenB}, acquireErr: map[string]error{cleanupTokenA: domain.ErrConflict}}
	files := nfoWriteFileCleanerFake(func(context.Context, domain.NFOWriteCommitCleanupInput) (domain.NFOWriteCommitCleanupOutcome, int, error) {
		return domain.NFOWriteCommitCleanupRetained, 1, nil
	})
	cleaner, err := NewNFOWriteCleaner(repo, files, cleanupOwner, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cleaned, err := cleaner.CleanPending(context.Background(), 10)
	if err != nil || cleaned != 1 || len(repo.completed) != 1 || repo.completed[cleanupTokenB] != domain.NFOWriteCommitCleanupRetained {
		t.Fatal("pending cleanup differs", cleaned, err, repo.completed)
	}
}

func TestNFOWriteCleanerLeavesFailedTokenUnrecorded(t *testing.T) {
	failure := errors.New("owned filesystem failure")
	repo := &nfoWriteCleanupFake{tokens: []string{cleanupTokenA, cleanupTokenB}}
	files := nfoWriteFileCleanerFake(func(_ context.Context, input domain.NFOWriteCommitCleanupInput) (domain.NFOWriteCommitCleanupOutcome, int, error) {
		if input.Lease.Token == cleanupTokenA {
			return 0, 0, failure
		}
		return domain.NFOWriteCommitCleanupRemoved, 0, nil
	})
	cleaner, err := NewNFOWriteCleaner(repo, files, cleanupOwner, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cleaned, err := cleaner.CleanPending(context.Background(), 10)
	if !errors.Is(err, failure) || cleaned != 1 {
		t.Fatal("failed token not reported after the rest", cleaned, err)
	}
	if _, ok := repo.completed[cleanupTokenA]; ok || repo.completed[cleanupTokenB] != domain.NFOWriteCommitCleanupRemoved {
		t.Fatal("failed token recorded or other token skipped", repo.completed)
	}
}

func TestNFOWriteCleanerStopsWhenLeaseIsLost(t *testing.T) {
	repo := &nfoWriteCleanupFake{renewErr: domain.ErrJobLeaseLost}
	files := nfoWriteFileCleanerFake(func(ctx context.Context, _ domain.NFOWriteCommitCleanupInput) (domain.NFOWriteCommitCleanupOutcome, int, error) {
		<-ctx.Done()
		return 0, 0, ctx.Err()
	})
	cleaner, err := NewNFOWriteCleaner(repo, files, cleanupOwner, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cleaner.Clean(context.Background(), cleanupTokenA); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("lost lease not reported", err)
	}
	if len(repo.completed) != 0 || repo.renewals != 1 {
		t.Fatal("cleanup recorded after losing the lease", repo.completed, repo.renewals)
	}
}

func TestNFOWriteCleanerRejectsInvalidConfiguration(t *testing.T) {
	files := nfoWriteFileCleanerFake(func(context.Context, domain.NFOWriteCommitCleanupInput) (domain.NFOWriteCommitCleanupOutcome, int, error) {
		return domain.NFOWriteCommitCleanupRemoved, 0, nil
	})
	for _, c := range []struct {
		repo  NFOWriteCleanupRepository
		files NFOWriteCommitFileCleaner
		owner string
		lease time.Duration
	}{{nil, files, cleanupOwner, time.Minute}, {&nfoWriteCleanupFake{}, nil, cleanupOwner, time.Minute}, {&nfoWriteCleanupFake{}, files, "owner", time.Minute}, {&nfoWriteCleanupFake{}, files, cleanupOwner, time.Second}} {
		if _, err := NewNFOWriteCleaner(c.repo, c.files, c.owner, c.lease); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("invalid cleaner configuration accepted")
		}
	}
}
