package postgres

import (
	"github.com/MoYuanCN/Jelee/internal/domain"
	"testing"
)

func TestIgnoreMetadataAdmission(t *testing.T) {
	for _, mode := range []string{"valid", "invalidated", "epoch", "revision"} {
		t.Run(mode, func(t *testing.T) {
			f, l, d, b := ignoreScanFixture(t)
			if _, err := f.s.LoadNFOWork(f.ctx, l); err != domain.ErrIgnoreUnavailable {
				t.Fatal("unfinished inventory admitted", err)
			}
			b.Inventory.Done = true
			if err := f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); err != nil {
				t.Fatal(err)
			}
			classifyForPublication(t, f, l, func(*domain.IgnoreBaselineDecision) {}, false)
			var query string
			switch mode {
			case "invalidated":
				query = `UPDATE job_ignore_manifests SET invalidated=true`
			case "epoch":
				query = `UPDATE libraries SET inventory_generation=inventory_generation+1`
			case "revision":
				query = `UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1`
			}
			if query != "" {
				if _, err := f.s.Pool.Exec(f.ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			_, nfoErr := f.s.LoadNFOWork(f.ctx, l)
			_, probeErr := f.s.LoadProbeWork(f.ctx, l)
			if mode == "valid" {
				if nfoErr != nil || probeErr != nil {
					t.Fatal("filtered inventory rejected", nfoErr, probeErr)
				}
			} else if nfoErr != domain.ErrInventoryInvalidated || probeErr != domain.ErrInventoryInvalidated {
				t.Fatal("stale inventory admitted", nfoErr, probeErr)
			}
			want := domain.ErrIgnoreUnavailable
			if mode == "epoch" {
				want = domain.ErrInventoryInvalidated
			}
			if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != want {
				t.Fatal("ordinary finish bypassed seal", err)
			}
		})
	}
}
