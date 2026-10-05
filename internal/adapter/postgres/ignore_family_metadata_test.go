package postgres

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"testing"
)

func TestFamilyMetadataAdmission(t *testing.T) {
	for _, mode := range []string{"valid", "incomplete", "custom-invalid", "legacy-invalid", "epoch", "revision", "cancel", "lease"} {
		t.Run(mode, func(t *testing.T) {
			var f jobFixture
			var l domain.JobLease
			if mode == "incomplete" {
				f, l = familyComparisonFixture(t)
			} else {
				f, l = familyVerificationFixture(t)
			}
			want := error(nil)
			query := ""
			id := l.Job.ID
			switch mode {
			case "incomplete":
				want = domain.ErrIgnoreUnavailable
			case "custom-invalid":
				query = `UPDATE job_ignore_manifests SET invalidated=true WHERE job_id=$1::uuid`
				want = domain.ErrInventoryInvalidated
			case "legacy-invalid":
				query = `UPDATE job_ignore_legacy_manifests SET invalidated=true WHERE job_id=$1::uuid`
				want = domain.ErrInventoryInvalidated
			case "epoch":
				query = `UPDATE libraries SET inventory_generation=inventory_generation+1 WHERE id=$1::uuid`
				id = l.Job.LibraryID
				want = domain.ErrInventoryInvalidated
			case "revision":
				query = `UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid`
				id = l.Job.LibraryID
				want = domain.ErrInventoryInvalidated
			case "cancel":
				query = `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`
				want = context.Canceled
			case "lease":
				l.Generation++
				want = domain.ErrJobLeaseLost
			}
			if query != "" {
				if _, err := f.s.Pool.Exec(f.ctx, query, id); err != nil {
					t.Fatal(err)
				}
			}
			_, nfoErr := f.s.LoadNFOWork(f.ctx, l)
			_, probeErr := f.s.LoadProbeWork(f.ctx, l)
			if nfoErr != want || probeErr != want {
				t.Fatal("metadata fence", nfoErr, probeErr)
			}
			if mode == "valid" {
				if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != domain.ErrIgnoreUnavailable {
					t.Fatal("ordinary finish bypassed family verification", err)
				}
			}
		})
	}
}
