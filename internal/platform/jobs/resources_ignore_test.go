package jobs

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"testing"
	"time"
)

type budgetFamilyRepo struct {
	app.FamilyIgnoreExecutionRepository
	visited bool
	saved   int
	fail    bool
}

func (f *budgetFamilyRepo) NextFamilyIgnoreScanDirectory(context.Context, domain.JobLease) (domain.ScanDirectory, error) {
	if f.visited {
		return domain.ScanDirectory{}, domain.ErrNotFound
	}
	f.visited = true
	return domain.ScanDirectory{Path: "."}, nil
}
func (f *budgetFamilyRepo) SaveFamilyIgnoreScanBatch(context.Context, domain.JobLease, domain.ScanDirectory, domain.FamilyIgnoreScanBatch) error {
	f.saved++
	if f.fail {
		return domain.ErrDatabase
	}
	return nil
}

type budgetFamilyScanner struct {
	app.FamilyIgnoreScanner
	scan func(context.Context, func(domain.FamilyIgnoreScanBatch) error) error
}

func (s budgetFamilyScanner) ScanFamilyIgnoreDirectory(ctx context.Context, _ domain.ScanDirectory, _ domain.IgnoreIntent, emit func(domain.FamilyIgnoreScanBatch) error) error {
	return s.scan(ctx, emit)
}

func TestFamilyInventoryBudgetReleasedAfterBatchFailure(t *testing.T) {
	for _, mode := range []string{"success", "storage", "missing-done", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
			repo := &budgetFamilyRepo{fail: mode == "storage"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			scanner := budgetFamilyScanner{scan: func(_ context.Context, emit func(domain.FamilyIgnoreScanBatch) error) error {
				if s := b.Stats(); s != (resources.Stats{IO: 1, Total: 1}) {
					t.Errorf("family scanner without IO: %+v", s)
				}
				if mode == "cancel" {
					cancel()
					return ctx.Err()
				}
				err := emit(domain.FamilyIgnoreScanBatch{Inventory: domain.ScanBatch{Done: mode != "missing-done"}})
				if mode == "storage" {
					return nil
				} // A scanner cannot hide a failed batch callback.
				return err
			}}
			r := &Runner{options: Options{Budget: b, DBOperationTimeout: time.Second, FamilyIgnore: &FamilyIgnoreOptions{Repository: repo, Scanner: scanner}}}
			err, storage := r.executeFamilyInventory(ctx, domain.JobLease{}, domain.IgnoreIntent{})
			switch mode {
			case "success":
				if err != nil || repo.saved != 1 {
					t.Fatal("family inventory failed")
				}
			case "storage":
				if err != domain.ErrDatabase || !storage {
					t.Fatal("lost storage failure")
				}
			case "missing-done":
				if err != domain.ErrScanIO {
					t.Fatal("accepted incomplete batch")
				}
			case "cancel":
				if err != context.Canceled || repo.saved != 0 {
					t.Fatal("cancel wrote batch")
				}
			}
			if b.Stats() != (resources.Stats{}) {
				t.Fatal("family inventory leaked permit")
			}
		})
	}
}

func TestFamilyVerificationReleasesBeforeCommit(t *testing.T) {
	for _, mode := range []string{"success", "observe-error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
			repo := &familyBatchRepo{}
			r := &Runner{options: Options{Budget: budget, DBOperationTimeout: time.Second, FamilyIgnore: &FamilyIgnoreOptions{Repository: repo}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pages, observed, committed := 0, 0, 0
			root := "00000000-0000-4000-8000-000000000001"
			err, storage := verifyFamilyStream(ctx, r, domain.JobLease{}, 128,
				func(context.Context) (int, []domain.IgnoreDirectoryProof, bool, error) {
					pages++
					return 1, []domain.IgnoreDirectoryProof{{RootID: root}}, pages > 1, nil
				},
				func(p domain.IgnoreDirectoryProof) string { return p.RootID },
				func(_ context.Context, _ string, p domain.IgnoreDirectoryProof) (domain.IgnoreDirectoryProof, error) {
					observed++
					if stats := budget.Stats(); stats != (resources.Stats{IO: 1, Total: 1}) {
						t.Errorf("proof without IO: %+v", stats)
					}
					if mode == "observe-error" {
						return p, domain.ErrIgnoreUnavailable
					}
					if mode == "cancel" {
						cancel()
						return p, ctx.Err()
					}
					return p, nil
				},
				func(context.Context, int, []domain.IgnoreDirectoryProof) error {
					committed++
					if budget.Stats() != (resources.Stats{}) {
						t.Error("proof held shared permit during commit")
					}
					return nil
				})
			if storage || observed != 1 || budget.Stats() != (resources.Stats{}) {
				t.Fatal("verification lost phase or leaked permit")
			}
			if mode == "success" {
				if err != nil || committed != 1 {
					t.Fatal("verification did not commit once")
				}
			} else if err == nil || committed != 0 {
				t.Fatal("failed observation was committed")
			}
		})
	}
}
