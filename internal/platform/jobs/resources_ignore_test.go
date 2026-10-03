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
