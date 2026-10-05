package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	consistencyJobID     = "00000000-0000-4000-8000-0000000000a1"
	consistencyLibraryID = "00000000-0000-4000-8000-0000000000a2"
)

// consistencyStoreFake is a checker repository with one empty library and
// the job finish of a consistency_check lease.
type consistencyStoreFake struct {
	mu       sync.Mutex
	startErr error
	started  []domain.ConsistencyRun
	finished []string
	block    chan struct{}
}

func (f *consistencyStoreFake) ConsistencyLibraries(context.Context, string) ([]domain.ConsistencyLibrary, error) {
	return []domain.ConsistencyLibrary{{ID: consistencyLibraryID}}, nil
}

func (f *consistencyStoreFake) ConsistencyPage(ctx context.Context, _ string, _ domain.ConsistencyScope, _ string) (domain.ConsistencyPage, error) {
	if f.block != nil {
		<-ctx.Done()
		return domain.ConsistencyPage{}, ctx.Err()
	}
	return domain.ConsistencyPage{}, nil
}

func (f *consistencyStoreFake) ConsistencyBaselineCurrent(context.Context, domain.ConsistencyLibrary) (bool, error) {
	return true, nil
}

func (f *consistencyStoreFake) StartConsistencyRun(_ context.Context, run domain.ConsistencyRun) (domain.ConsistencyRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, run)
	run.ID = "00000000-0000-4000-8000-0000000000a3"
	return run, f.startErr
}

func (f *consistencyStoreFake) FinishConsistencyRun(_ context.Context, report domain.ConsistencyReport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, report.State)
	return nil
}

type consistencyFilesFake struct{}

func (consistencyFilesFake) FileExists(context.Context, string, string) (bool, error) {
	return false, nil
}

type consistencyPathsFake struct{}

func (consistencyPathsFake) RenderPath(string, string) string { return "[redacted]" }

type consistencyFinishFake struct {
	mu    sync.Mutex
	calls [][2]string
}

func (f *consistencyFinishFake) FinishConsistencyCheck(_ context.Context, _ domain.JobLease, state, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [2]string{state, code})
	return nil
}

func consistencyRunner(t *testing.T, store *consistencyStoreFake, finish *consistencyFinishFake, claim func(context.Context, domain.ScanCapabilities) (domain.JobLease, error)) *Runner {
	t.Helper()
	checker, err := app.NewConsistencyChecker(store, consistencyFilesFake{}, nil, consistencyPathsFake{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := claimStagesFake{JobExecutionRepository: &executionFake{
		heartbeat: func(context.Context, domain.JobLease, time.Duration) (bool, error) { return false, nil },
		release:   func(context.Context, domain.JobLease) error { return nil },
		finish: func(context.Context, domain.JobLease, string, string) error {
			t.Error("a consistency job finished through the scan path")
			return nil
		},
	}, claim: claim}
	opts := DefaultOptions()
	opts.Workers = 1
	opts.Consistency = &ConsistencyOptions{Checker: checker, Repository: finish, Template: app.ConsistencyOptions{StatBudget: 5}}
	r, err := New(repo, scannerFunc(nil), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func consistencyLease() domain.JobLease {
	return domain.JobLease{Job: domain.Job{ID: consistencyJobID, LibraryID: consistencyLibraryID, Kind: domain.JobConsistencyCheck, State: domain.JobRunning}, Owner: "worker", Generation: 1, Policy: domain.JobPolicy{HistoryLimit: 10}}
}

func TestConsistencyJobRunsTheCheckerUnderTheLease(t *testing.T) {
	store, finish := &consistencyStoreFake{}, &consistencyFinishFake{}
	r := consistencyRunner(t, store, finish, nil)
	r.run(context.Background(), consistencyLease())
	if len(store.started) != 1 || store.started[0].Origin != domain.ConsistencyOriginJob || store.started[0].JobID != consistencyJobID || store.started[0].LibraryID != consistencyLibraryID || store.started[0].Mode != domain.ConsistencyModeReport {
		t.Fatalf("run identity: %+v", store.started)
	}
	if len(finish.calls) != 1 || finish.calls[0] != [2]string{domain.JobSucceeded, ""} || len(store.finished) != 1 || store.finished[0] != domain.ConsistencyRunCompleted {
		t.Fatalf("finish: %v report %v", finish.calls, store.finished)
	}
}

func TestConsistencyJobFailureUsesItsOwnCode(t *testing.T) {
	store, finish := &consistencyStoreFake{startErr: domain.ErrDatabase}, &consistencyFinishFake{}
	consistencyRunner(t, store, finish, nil).run(context.Background(), consistencyLease())
	if len(finish.calls) != 1 || finish.calls[0] != [2]string{domain.JobFailed, "consistency_check_failed"} {
		t.Fatalf("finish: %v", finish.calls)
	}
}

func TestConsistencyJobLocalCancellationPersistsTheReport(t *testing.T) {
	store, finish := &consistencyStoreFake{block: make(chan struct{})}, &consistencyFinishFake{}
	r := consistencyRunner(t, store, finish, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.run(context.Background(), consistencyLease())
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.cancellationMu.Lock()
		_, registered := r.runningCancellations[consistencyJobID]
		r.cancellationMu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job never registered for cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	r.NotifyJobCancellation(consistencyJobID)
	<-done
	if len(finish.calls) != 1 || finish.calls[0][0] != domain.JobCancelled || len(store.finished) != 1 || store.finished[0] != domain.ConsistencyRunCancelled {
		t.Fatalf("cancelled job: %v report %v", finish.calls, store.finished)
	}
}

func TestConsistencyJobClaimAdvertisesTheChecker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var advertised bool
	claim := func(_ context.Context, caps domain.ScanCapabilities) (domain.JobLease, error) {
		advertised = caps.ConsistencyCheck
		cancel()
		return domain.JobLease{}, domain.ErrNotFound
	}
	consistencyRunner(t, &consistencyStoreFake{}, &consistencyFinishFake{}, claim).work(ctx)
	if !advertised {
		t.Fatal("a worker with a checker did not claim consistency jobs")
	}
}

func TestConsistencyJobOptionsValidation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := DefaultOptions()
	opts.Consistency = &ConsistencyOptions{Repository: &consistencyFinishFake{}}
	if _, err := New(claimStagesFake{JobExecutionRepository: &executionFake{}}, scannerFunc(nil), opts, logger); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("checker-less options accepted")
	}
	checker, err := app.NewConsistencyChecker(&consistencyStoreFake{}, consistencyFilesFake{}, nil, consistencyPathsFake{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	opts.Consistency = &ConsistencyOptions{Checker: checker, Repository: &consistencyFinishFake{}}
	if _, err := New(&executionFake{}, scannerFunc(nil), opts, logger); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("a repository that cannot claim by capability accepted")
	}
	// Without the option a claimed consistency job cannot run or finish.
	r, err := New(claimStagesFake{JobExecutionRepository: &executionFake{
		heartbeat: func(context.Context, domain.JobLease, time.Duration) (bool, error) { return false, nil },
	}}, scannerFunc(nil), DefaultOptions(), logger)
	if err != nil {
		t.Fatal(err)
	}
	if storage, err := r.executeConsistency(context.Background(), consistencyLease()); storage || !errors.Is(err, domain.ErrScanUnavailable) {
		t.Fatal("unconfigured runner executed a consistency job", err)
	}
	if err := r.finishJob(context.Background(), consistencyLease(), domain.JobFailed, "consistency_check_failed"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unconfigured runner finished a consistency job", err)
	}
}
