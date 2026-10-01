package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type ignoreRunFake struct {
	app.IgnoreExecutionRepository
	progress                    domain.IgnoreExecutionProgress
	verification, sealed, saved int
	next                        int
	saveError                   error
}

func (f *ignoreRunFake) ReadIgnoreProgress(context.Context, domain.JobLease) (domain.IgnoreExecutionProgress, error) {
	return f.progress, nil
}
func (f *ignoreRunFake) NextIgnoreBaselinePage(context.Context, domain.JobLease) (domain.IgnoreBaselinePage, error) {
	return domain.IgnoreBaselinePage{Complete: true}, nil
}
func (f *ignoreRunFake) BeginIgnoreVerification(context.Context, domain.JobLease) error {
	f.verification++
	return nil
}
func (f *ignoreRunFake) NextIgnoreVerificationPage(context.Context, domain.JobLease) (domain.IgnoreVerificationPage, error) {
	return domain.IgnoreVerificationPage{Complete: true}, nil
}
func (f *ignoreRunFake) SealIgnoreVerification(context.Context, domain.JobLease) error {
	f.sealed++
	return nil
}
func (f *ignoreRunFake) NextIgnoreScanDirectory(context.Context, domain.JobLease) (domain.ScanDirectory, error) {
	f.next++
	if f.next > 1 {
		return domain.ScanDirectory{}, domain.ErrNotFound
	}
	return domain.ScanDirectory{}, nil
}
func (f *ignoreRunFake) SaveIgnoreScanBatch(context.Context, domain.JobLease, domain.ScanDirectory, domain.IgnoreScanBatch) error {
	f.saved++
	return f.saveError
}

type ignoreScannerFunc func(context.Context, domain.ScanDirectory, domain.IgnoreIntent, func(domain.IgnoreScanBatch) error) error

func (f ignoreScannerFunc) ScanIgnoreDirectory(c context.Context, d domain.ScanDirectory, i domain.IgnoreIntent, emit func(domain.IgnoreScanBatch) error) error {
	return f(c, d, i, emit)
}

func TestIgnoreRunnerRecoveryUsesPersistedUnknown(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		f := &ignoreRunFake{progress: domain.IgnoreExecutionProgress{ComparisonStarted: true, Unknown: unknown}}
		r := &Runner{options: Options{DBOperationTimeout: time.Second, Ignore: &IgnoreOptions{Repository: f}}}
		// No ordinary scanner or metadata repository is installed: a fallback or
		// unnecessary inventory restart would panic instead of passing this test.
		err, _ := r.executeIgnore(context.Background(), domain.JobLease{}, domain.IgnoreRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if unknown && (f.verification != 0 || f.sealed != 0) {
			t.Fatal("unknown review attempted verification")
		}
		if !unknown && (f.verification != 1 || f.sealed != 1) {
			t.Fatal("known inventory omitted verification")
		}
		if f.next != 0 {
			t.Fatal("frozen inventory restarted")
		}
	}
}

func TestIgnoreRunnerBatchFailureAndCompletion(t *testing.T) {
	sentinel := errors.New("database rejected batch")
	for _, mode := range []string{"save-failure", "missing-done", "after-done", "success"} {
		t.Run(mode, func(t *testing.T) {
			f := &ignoreRunFake{}
			if mode == "save-failure" {
				f.saveError = sentinel
			}
			scanner := ignoreScannerFunc(func(_ context.Context, _ domain.ScanDirectory, _ domain.IgnoreIntent, emit func(domain.IgnoreScanBatch) error) error {
				b := domain.IgnoreScanBatch{Inventory: domain.ScanBatch{Done: mode != "missing-done"}}
				err := emit(b)
				if mode == "after-done" {
					return emit(b)
				}
				if mode == "save-failure" {
					return nil
				} // Even a buggy scanner cannot hide DB failure.
				return err
			})
			r := &Runner{options: Options{DBOperationTimeout: time.Second, Ignore: &IgnoreOptions{Repository: f, Scanner: scanner}}}
			err, storage := r.executeIgnoreInventory(context.Background(), domain.JobLease{}, domain.IgnoreIntent{})
			switch mode {
			case "save-failure":
				if err != sentinel || !storage {
					t.Fatal("lost DB failure", err, storage)
				}
			case "missing-done", "after-done":
				if err != domain.ErrScanIO {
					t.Fatal("incomplete/duplicate completion accepted", err)
				}
			case "success":
				if err != nil || f.next != 2 {
					t.Fatal("completed inventory failed", err)
				}
			}
			if f.saved != 1 {
				t.Fatal("unexpected persisted batches", f.saved)
			}
		})
	}
}

type ignoreTerminalFake struct {
	*executionFake
	app.IgnoreExecutionRepository
	request   *domain.IgnoreRequest
	published int
}

func (f *ignoreTerminalFake) ReadIgnoreRequest(context.Context, domain.JobLease) (*domain.IgnoreRequest, error) {
	return f.request, nil
}
func (f *ignoreTerminalFake) FinishIgnoreJob(context.Context, domain.JobLease) error {
	f.published++
	return nil
}

func TestIgnoreRunnerTerminalRouting(t *testing.T) {
	for _, state := range []string{domain.JobSucceeded, domain.JobFailed, domain.JobCancelled} {
		ordinary := 0
		f := &ignoreTerminalFake{request: &domain.IgnoreRequest{}, executionFake: &executionFake{finish: func(context.Context, domain.JobLease, string, string) error { ordinary++; return nil }}}
		r := &Runner{repository: f, options: Options{Ignore: &IgnoreOptions{Repository: f}}}
		if err := r.finishJob(context.Background(), domain.JobLease{}, state, ""); err != nil {
			t.Fatal(err)
		}
		if state == domain.JobSucceeded {
			if f.published != 1 || ordinary != 0 {
				t.Fatal("enabled success bypassed seal")
			}
		} else if f.published != 0 || ordinary != 1 {
			t.Fatal("failure entered publication")
		}
	}
}
