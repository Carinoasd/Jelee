package jobs

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"io"
	"log/slog"
	"testing"
	"time"
)

type familyRunFake struct {
	app.FamilyIgnoreExecutionRepository
	unknown                                 bool
	begun, sealed, custom, legacy, baseline int
	legacyError                             error
}

func (f *familyRunFake) ReadFamilyIgnoreProgress(context.Context, domain.JobLease) (domain.IgnoreExecutionProgress, error) {
	return domain.IgnoreExecutionProgress{ComparisonStarted: true, Unknown: f.unknown}, nil
}
func (f *familyRunFake) NextFamilyIgnoreBaselinePage(context.Context, domain.JobLease) (domain.IgnoreBaselinePage, error) {
	return domain.IgnoreBaselinePage{Complete: true}, nil
}
func (f *familyRunFake) BeginFamilyIgnoreVerification(context.Context, domain.JobLease) error {
	f.begun++
	return nil
}
func (f *familyRunFake) NextFamilyIgnoreVerificationPage(context.Context, domain.JobLease) (domain.IgnoreVerificationPage, error) {
	f.custom++
	return domain.IgnoreVerificationPage{Complete: true}, nil
}
func (f *familyRunFake) NextLegacyIgnoreVerificationPage(context.Context, domain.JobLease) (domain.LegacyIgnoreVerificationPage, error) {
	f.legacy++
	return domain.LegacyIgnoreVerificationPage{Complete: true}, f.legacyError
}
func (f *familyRunFake) NextLegacyIgnoreBaselineVerificationPage(context.Context, domain.JobLease) (domain.LegacyIgnoreBaselineVerificationPage, error) {
	f.baseline++
	return domain.LegacyIgnoreBaselineVerificationPage{Complete: true}, nil
}
func (f *familyRunFake) SealFamilyIgnoreVerification(context.Context, domain.JobLease) error {
	f.sealed++
	return nil
}

type familyScannerStub struct{ app.FamilyIgnoreScanner }

func TestFamilyRunnerPersistedReviewAndThreeStreams(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		f := &familyRunFake{unknown: unknown}
		r := &Runner{options: Options{DBOperationTimeout: time.Second, FamilyIgnore: &FamilyIgnoreOptions{Repository: f, Scanner: &familyScannerStub{}}}}
		if err, _ := r.executeFamilyIgnore(context.Background(), domain.JobLease{}, domain.IgnoreRequest{}); err != nil {
			t.Fatal(err)
		}
		want := 1
		if unknown {
			want = 0
		}
		if f.begun != want || f.custom != want || f.legacy != want || f.baseline != want || f.sealed != want {
			t.Fatal("review or stream order bypassed")
		}
	}
	f := &familyRunFake{legacyError: domain.ErrDatabase}
	r := &Runner{options: Options{DBOperationTimeout: time.Second, FamilyIgnore: &FamilyIgnoreOptions{Repository: f, Scanner: &familyScannerStub{}}}}
	if err, storage := r.executeFamilyIgnore(context.Background(), domain.JobLease{}, domain.IgnoreRequest{}); err != domain.ErrDatabase || !storage || f.baseline != 0 || f.sealed != 0 {
		t.Fatal("failed source stream bypassed", err)
	}
}

type familyOnlyExecutionFake struct {
	executionFake
	app.FamilyIgnoreExecutionRepository
}
type inventoryStub struct{ app.InventoryScanner }

func TestFamilyRunnerRequiresDispatchRepository(t *testing.T) {
	opts := DefaultOptions()
	opts.FamilyIgnore = &FamilyIgnoreOptions{Repository: &familyRunFake{}, Scanner: &familyScannerStub{}}
	if _, err := New(&familyOnlyExecutionFake{}, &inventoryStub{}, opts, slog.New(slog.NewTextHandler(io.Discard, nil))); err != domain.ErrInvalid {
		t.Fatal("family option could fall back to ordinary inventory", err)
	}
}

func TestFamilyRunnerRechecksReadinessAtClaims(t *testing.T) {
	for _, withNFO := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "nfo"}[withNFO], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready, calls := true, 0
			claim := func(_ context.Context, c domain.ScanCapabilities) (domain.JobLease, error) {
				calls++
				if c.FamilyIgnore != ready {
					t.Error("claim used stale helper readiness")
				}
				ready = false
				cancel()
				return domain.JobLease{}, domain.ErrNotFound
			}
			r := &Runner{repository: claimStagesFake{claim: claim}, options: DefaultOptions(), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			r.options.FamilyIgnore = &FamilyIgnoreOptions{Repository: &familyRunFake{}, Scanner: &familyScannerStub{}, Available: func() bool { return ready }}
			if withNFO {
				r.nfoRepository = familyNFOClaimFake{claimStagesFake: claimStagesFake{claim: claim}}
			}
			r.work(ctx)
			if calls != 1 {
				t.Fatal("claim not invoked")
			}
			ctx, cancel = context.WithCancel(context.Background())
			defer cancel()
			r.work(ctx)
			if calls != 2 {
				t.Fatal("disabled capability was not passed to storage")
			}
		})
	}
}

type familyNFOClaimFake struct {
	app.NFOExecutionRepository
	claimStagesFake
}

func (f familyNFOClaimFake) ClaimJobWithCapabilities(c context.Context, o string, b bool, d time.Duration, v domain.ScanCapabilities) (domain.JobLease, error) {
	return f.claimStagesFake.ClaimJobWithCapabilities(c, o, b, d, v)
}
