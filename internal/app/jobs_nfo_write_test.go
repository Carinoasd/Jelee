package app

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type nfoWriteJobsFake struct {
	calls int
	got   []string
}

func (f *nfoWriteJobsFake) SubmitNFOWriteJob(_ context.Context, _ domain.Actor, library, _, priority string, preparations []string, policy domain.JobPolicy) (domain.Job, bool, error) {
	f.calls++
	f.got = preparations
	return domain.Job{ID: library, Kind: domain.JobNFOWrite, Priority: priority, State: domain.JobQueued}, policy != jobTestPolicy(), nil
}

func TestJobsSubmitNFOWriteValidatesBeforeTheRepository(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const other = "22222222-2222-4222-8222-222222222222"
	actor := domain.Actor{UserID: id, SessionID: id}
	repo := &nfoWriteJobsFake{}
	base := newJobService(t, jobRepositoryFake{})
	if _, err := NewJobsWithNFOWrite(nil, repo); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil base accepted")
	}
	if _, err := NewJobsWithNFOWrite(base, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil repository accepted")
	}
	// Without a repository no library is writable.
	if _, _, err := base.SubmitNFOWrite(context.Background(), actor, id, "k", domain.JobPriorityManual, []string{id}); !errors.Is(err, domain.ErrNFOWriteDisabled) {
		t.Fatal("missing repository", err)
	}
	jobs, err := NewJobsWithNFOWrite(base, repo)
	if err != nil {
		t.Fatal(err)
	}
	many := make([]string, NFOWriteBatchMax+1)
	for i := range many {
		many[i] = id
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, call := range map[string]func() error{
		"nil context": func() error {
			_, _, err := jobs.SubmitNFOWrite(nil, actor, id, "k", "manual", []string{id})
			return err
		}, //nolint:staticcheck // SA1012: the nil context is the case under test
		"bad actor": func() error {
			_, _, err := jobs.SubmitNFOWrite(context.Background(), domain.Actor{}, id, "k", "manual", []string{id})
			return err
		},
		"bad library": func() error {
			_, _, err := jobs.SubmitNFOWrite(context.Background(), actor, "x", "k", "manual", []string{id})
			return err
		},
		"bad key": func() error {
			_, _, err := jobs.SubmitNFOWrite(context.Background(), actor, id, "a b", "manual", []string{id})
			return err
		},
		"bad priority": func() error {
			_, _, err := jobs.SubmitNFOWrite(context.Background(), actor, id, "k", "urgent", []string{id})
			return err
		},
		"empty": func() error {
			_, _, err := jobs.SubmitNFOWrite(context.Background(), actor, id, "k", "manual", nil)
			return err
		},
		"too many": func() error {
			_, _, err := jobs.SubmitNFOWrite(context.Background(), actor, id, "k", "manual", many)
			return err
		},
		"bad preparation": func() error {
			_, _, err := jobs.SubmitNFOWrite(context.Background(), actor, id, "k", "manual", []string{"x"})
			return err
		},
		"duplicate": func() error {
			_, _, err := jobs.SubmitNFOWrite(context.Background(), actor, id, "k", "manual", []string{id, id})
			return err
		},
	} {
		if err := call(); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal(name, err)
		}
	}
	if _, _, err := jobs.SubmitNFOWrite(canceled, actor, id, "k", "manual", []string{id}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled context reached the repository", err)
	}
	if repo.calls != 0 {
		t.Fatal("rejected input reached the repository")
	}
	input := []string{id, other}
	job, replay, err := jobs.SubmitNFOWrite(context.Background(), actor, id, "k", domain.JobPriorityBackground, input)
	if err != nil || replay || repo.calls != 1 || job.Kind != domain.JobNFOWrite || job.Priority != domain.JobPriorityBackground {
		t.Fatal("valid submission", err)
	}
	input[0] = "mutated"
	if repo.got[0] != id {
		t.Fatal("repository shares the caller's slice")
	}
}
