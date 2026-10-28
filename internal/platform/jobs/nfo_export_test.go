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

const nfoExportJobID = "00000000-0000-4000-8000-0000000000b1"

type nfoExportStepFake struct {
	run func(context.Context, domain.JobLease) error
}

func (f nfoExportStepFake) Run(ctx context.Context, l domain.JobLease) error { return f.run(ctx, l) }

// nfoExportRepoFake records how a run ends: finished, yielded or released.
type nfoExportRepoFake struct {
	app.NFOExportExecutionRepository
	mu       sync.Mutex
	finished [][2]string
	yields   int
	yieldErr error
}

func (f *nfoExportRepoFake) FinishNFOExport(_ context.Context, _ domain.JobLease, state, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, [2]string{state, code})
	return nil
}

func (f *nfoExportRepoFake) YieldNFOExport(context.Context, domain.JobLease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.yields++
	return f.yieldErr
}

func nfoExportLease() domain.JobLease {
	return domain.JobLease{Job: domain.Job{ID: nfoExportJobID, LibraryID: consistencyLibraryID, Kind: domain.JobNFOExport, State: domain.JobRunning}, Owner: "worker", Generation: 1, Policy: domain.JobPolicy{HistoryLimit: 10}}
}

// nfoExportRunner builds a runner whose scan path fails the test: an export
// ends only through its own repository.
func nfoExportRunner(t *testing.T, step func(context.Context, domain.JobLease) error, repo *nfoExportRepoFake, claim func(context.Context, domain.ScanCapabilities) (domain.JobLease, error)) *Runner {
	t.Helper()
	execution := claimStagesFake{JobExecutionRepository: &executionFake{
		heartbeat: func(context.Context, domain.JobLease, time.Duration) (bool, error) { return false, nil },
		release: func(context.Context, domain.JobLease) error {
			t.Error("an export was released instead of yielded or finished")
			return nil
		},
		finish: func(context.Context, domain.JobLease, string, string) error {
			t.Error("an export finished through the scan path")
			return nil
		},
	}, claim: claim}
	opts := DefaultOptions()
	opts.Workers = 1
	r, err := New(execution, scannerFunc(nil), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// The write runner an export needs is not part of this test.
	r.options.NFOExport = &NFOExportOptions{Worker: nfoExportStepFake{step}, Repository: repo}
	return r
}

func TestNFOExportYieldReturnsTheJobToTheQueueInsteadOfFinishing(t *testing.T) {
	repo := &nfoExportRepoFake{}
	r := nfoExportRunner(t, func(context.Context, domain.JobLease) error { return app.ErrNFOExportYield }, repo, nil)
	r.run(context.Background(), nfoExportLease())
	if repo.yields != 1 || len(repo.finished) != 0 {
		t.Fatalf("yields=%d finished=%v", repo.yields, repo.finished)
	}
	// A failing yield leaves the job to lease expiry; it is not finished either.
	repo = &nfoExportRepoFake{yieldErr: domain.ErrJobLeaseLost}
	r = nfoExportRunner(t, func(context.Context, domain.JobLease) error { return app.ErrNFOExportYield }, repo, nil)
	r.run(context.Background(), nfoExportLease())
	if repo.yields != 1 || len(repo.finished) != 0 {
		t.Fatalf("failed yield: yields=%d finished=%v", repo.yields, repo.finished)
	}
}

func TestNFOExportRunFinishesWithItsOwnCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want [2]string
	}{
		{"done", nil, [2]string{domain.JobSucceeded, ""}},
		{"storage failure", domain.ErrDatabase, [2]string{domain.JobFailed, "nfo_export_failed"}},
		{"stopped item error", domain.ErrNFOWriteDisabled, [2]string{domain.JobFailed, "nfo_export_failed"}},
		{"cancelled", context.Canceled, [2]string{domain.JobCancelled, ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &nfoExportRepoFake{}
			nfoExportRunner(t, func(context.Context, domain.JobLease) error { return tc.err }, repo, nil).run(context.Background(), nfoExportLease())
			if len(repo.finished) != 1 || repo.finished[0] != tc.want || repo.yields != 0 {
				t.Fatalf("finished=%v yields=%d", repo.finished, repo.yields)
			}
		})
	}
}

func TestNFOExportLocalCancellationBeatsAYield(t *testing.T) {
	repo := &nfoExportRepoFake{}
	started := make(chan struct{})
	r := nfoExportRunner(t, func(ctx context.Context, _ domain.JobLease) error {
		close(started)
		<-ctx.Done()
		// The step had already submitted its child when the cancel arrived.
		return app.ErrNFOExportYield
	}, repo, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.run(context.Background(), nfoExportLease())
	}()
	<-started
	r.NotifyJobCancellation(nfoExportJobID)
	<-done
	if repo.yields != 0 || len(repo.finished) != 1 || repo.finished[0][0] != domain.JobCancelled {
		t.Fatalf("a cancelled export must finish as cancelled: yields=%d finished=%v", repo.yields, repo.finished)
	}
}

func TestNFOExportClaimAdvertisesTheExporterOnlyWhenConfigured(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var advertised []bool
	claim := func(_ context.Context, caps domain.ScanCapabilities) (domain.JobLease, error) {
		advertised = append(advertised, caps.NFOExport)
		cancel()
		return domain.JobLease{}, domain.ErrNotFound
	}
	nfoExportRunner(t, nil, &nfoExportRepoFake{}, claim).work(ctx)
	if len(advertised) != 1 || !advertised[0] {
		t.Fatal("a worker with an exporter did not claim export jobs", advertised)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	plain, err := New(claimStagesFake{JobExecutionRepository: &executionFake{}, claim: func(_ context.Context, caps domain.ScanCapabilities) (domain.JobLease, error) {
		if caps.NFOExport {
			t.Error("a worker without an exporter claims export jobs")
		}
		cancel2()
		return domain.JobLease{}, domain.ErrNotFound
	}}, scannerFunc(nil), DefaultOptions(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	plain.work(ctx2)
}

func TestNFOExportOptionsValidation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	step := nfoExportStepFake{func(context.Context, domain.JobLease) error { return nil }}
	for name, opts := range map[string]*NFOExportOptions{"no worker": {Repository: &nfoExportRepoFake{}}, "no repository": {Worker: step}, "no write runner": {Worker: step, Repository: &nfoExportRepoFake{}}} {
		o := DefaultOptions()
		o.NFOExport = opts
		if _, err := New(claimStagesFake{JobExecutionRepository: &executionFake{}}, scannerFunc(nil), o, logger); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal(name, "accepted", err)
		}
	}
	// Without the option a claimed export cannot run or finish.
	r, err := New(claimStagesFake{JobExecutionRepository: &executionFake{}}, scannerFunc(nil), DefaultOptions(), logger)
	if err != nil {
		t.Fatal(err)
	}
	if storage, err := r.executeNFOExport(context.Background(), nfoExportLease()); storage || !errors.Is(err, domain.ErrScanUnavailable) {
		t.Fatal("unconfigured runner executed an export", err)
	}
	if err := r.finishJob(context.Background(), nfoExportLease(), domain.JobFailed, "nfo_export_failed"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unconfigured runner finished an export", err)
	}
}
