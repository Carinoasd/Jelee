package postgres

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"testing"
)

func TestFamilyExecutionPrivateReads(t *testing.T) {
	f, l := familyComparisonFixture(t)
	root, err := f.s.ReadFamilyIgnoreRoot(f.ctx, l, f.registration.RootID)
	if err != nil || root == "" {
		t.Fatal("root unavailable", err)
	}
	p, err := f.s.ReadFamilyIgnoreProgress(f.ctx, l)
	if err != nil || p.ComparisonStarted || p.Unknown {
		t.Fatal("initial progress", err)
	}
	if err = f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.ReadFamilyIgnoreProgress(f.ctx, l)
	if err != nil || !p.ComparisonStarted || p.Unknown {
		t.Fatal("comparison progress", err)
	}
	other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if v, e := f.s.ReadFamilyIgnoreRoot(f.ctx, l, other.RootID); e != domain.ErrNotFound || v != "" {
		t.Fatal("foreign root exposed", e)
	}
	if _, e := f.s.ReadIgnoreRoot(f.ctx, l, f.registration.RootID); e != domain.ErrConflict {
		t.Fatal("old root reader accepted family", e)
	}
	if _, e := f.s.ReadIgnoreProgress(f.ctx, l); e != domain.ErrConflict {
		t.Fatal("old progress reader accepted family", e)
	}
}

func TestFamilyExecutionPrivateReadFences(t *testing.T) {
	for _, kind := range []string{"generation", "cancel", "epoch", "custom-invalid", "legacy-invalid"} {
		t.Run(kind, func(t *testing.T) {
			f, l := familyComparisonFixture(t)
			var query string
			id := l.Job.ID
			want := error(domain.ErrInventoryInvalidated)
			switch kind {
			case "generation":
				l.Generation++
				want = domain.ErrJobLeaseLost
			case "cancel":
				query = `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`
				want = context.Canceled
			case "epoch":
				query = `UPDATE libraries SET inventory_generation=inventory_generation+1 WHERE id=$1::uuid`
				id = l.Job.LibraryID
			case "custom-invalid":
				query = `UPDATE job_ignore_manifests SET invalidated=true WHERE job_id=$1::uuid`
			case "legacy-invalid":
				query = `UPDATE job_ignore_legacy_manifests SET invalidated=true WHERE job_id=$1::uuid`
			}
			if query != "" {
				if _, err := f.s.Pool.Exec(f.ctx, query, id); err != nil {
					t.Fatal(err)
				}
			}
			if v, e := f.s.ReadFamilyIgnoreRoot(f.ctx, l, f.registration.RootID); e != want || v != "" {
				t.Fatal("root fence", e)
			}
			if v, e := f.s.ReadFamilyIgnoreProgress(f.ctx, l); e != want || v != (domain.IgnoreExecutionProgress{}) {
				t.Fatal("progress fence", e)
			}
		})
	}
}

func TestFamilyExecutionRequestDispatchRead(t *testing.T) {
	f, l, _ := legacyManifestFixture(t)
	r, err := f.s.ReadExecutionIgnoreRequest(f.ctx, l)
	if err != nil || r == nil || domain.ValidateFamilyIgnoreRequest(*r) != nil || r.JobID != l.Job.ID || r.LibraryID != l.Job.LibraryID {
		t.Fatal("retained request unavailable", err)
	}
	if v, e := f.s.ReadIgnoreRequest(f.ctx, l); e != domain.ErrConflict || v != nil {
		t.Fatal("old reader accepted family", e)
	}
	l.Generation++
	if v, e := f.s.ReadExecutionIgnoreRequest(f.ctx, l); e != domain.ErrJobLeaseLost || v != nil {
		t.Fatal("stale request exposed", e)
	}
	f2, l2, _ := manifestFixture(t)
	if v, e := f2.s.ReadExecutionIgnoreRequest(f2.ctx, l2); e != nil || v == nil || domain.ValidateIgnoreRequest(*v) != nil {
		t.Fatal("old request unavailable", e)
	}
	if _, e := f2.s.Pool.Exec(f2.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l2.Job.ID); e != nil {
		t.Fatal(e)
	}
	if v, e := f2.s.ReadExecutionIgnoreRequest(f2.ctx, l2); e != context.Canceled || v != nil {
		t.Fatal("cancelled request exposed", e)
	}
}
