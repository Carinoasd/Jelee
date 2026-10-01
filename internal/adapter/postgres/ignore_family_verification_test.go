package postgres

import (
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func familyVerificationFixture(t *testing.T) (jobFixture, domain.JobLease) {
	t.Helper()
	f, l := familyComparisonFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM library_inventory_baseline WHERE library_id=$1::uuid`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.NextFamilyIgnoreBaselinePage(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, nil); err != nil {
		t.Fatal(err)
	}
	return f, l
}

func familyVerificationSnapshot(t *testing.T, f jobFixture, l domain.JobLease) (time.Time, int64) {
	t.Helper()
	var deadline time.Time
	var sequence int64
	var valid bool
	err := f.s.Pool.QueryRow(f.ctx, `SELECT c.deadline,s.sequence,c.generation=$2 AND s.generation=$2 AND b.generation=$2 AND c.deadline=s.deadline AND c.deadline=b.deadline AND m.frozen AND n.frozen FROM job_ignore_verifications c JOIN job_ignore_legacy_verifications s USING(job_id) JOIN job_ignore_legacy_baseline_verifications b USING(job_id) JOIN job_ignore_manifests m USING(job_id) JOIN job_ignore_legacy_manifests n USING(job_id) WHERE c.job_id=$1::uuid`, l.Job.ID, l.Generation).Scan(&deadline, &sequence, &valid)
	if err != nil || !valid {
		t.Fatal("checkpoints not coordinated", err)
	}
	return deadline, sequence
}

func TestFamilyVerificationBeginRetryAndGeneration(t *testing.T) {
	f, l := familyVerificationFixture(t)
	if err := f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	initial, _ := familyVerificationSnapshot(t, f, l)
	p, err := f.s.NextLegacyIgnoreVerificationPage(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, p.Token, p.Observations); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '10 minutes' WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	deadline, sequence := familyVerificationSnapshot(t, f, l)
	if !deadline.Equal(initial) || sequence != 1 {
		t.Fatal("retry renewed or restarted evidence")
	}
	fresh, err := scanLease(f.s.Pool.QueryRow(f.ctx, `UPDATE jobs SET generation=generation+1 WHERE id=$1::uuid RETURNING `+leaseColumns, l.Job.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.BeginFamilyIgnoreVerification(f.ctx, fresh); err != nil {
		t.Fatal(err)
	}
	_, sequence = familyVerificationSnapshot(t, f, fresh)
	if sequence != 0 {
		t.Fatal("new generation inherited progress")
	}
	if err = f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != domain.ErrJobLeaseLost {
		t.Fatal("old lease survived", err)
	}
}

func TestFamilyVerificationDoesNotRenewIndependentLegacyDeadline(t *testing.T) {
	f, l := familyVerificationFixture(t)
	if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginLegacyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	var original time.Time
	if err := f.s.Pool.QueryRow(f.ctx, `UPDATE job_ignore_legacy_verifications SET deadline=clock_timestamp()+interval '20 seconds' WHERE job_id=$1::uuid RETURNING deadline`, l.Job.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	deadline, _ := familyVerificationSnapshot(t, f, l)
	if !deadline.Equal(original) {
		t.Fatal("existing deadline extended")
	}
	for _, table := range []string{"job_ignore_verifications", "job_ignore_legacy_verifications", "job_ignore_legacy_baseline_verifications"} {
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE `+table+` SET deadline='2000-01-01' WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != domain.ErrInventoryInvalidated {
		t.Fatal("expired coordinator renewed", err)
	}
}

func TestFamilyVerificationRequiresCompleteKnownComparison(t *testing.T) {
	f, l := familyComparisonFixture(t)
	if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("incomplete comparison accepted", err)
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_verifications WHERE job_id=$1::uuid`, l.Job.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial checkpoint retained", err)
	}
}

func TestFamilyVerificationCustomPagesAndInvalidation(t *testing.T) {
	for _, mode := range []string{"changed", "deadline", "missing-checkpoint", "legacy-invalid"} {
		t.Run(mode, func(t *testing.T) {
			f, l := familyVerificationFixture(t)
			if err := f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			p, err := f.s.NextFamilyIgnoreVerificationPage(f.ctx, l)
			if err != nil || len(p.Proofs) != 1 {
				t.Fatal("custom page", err)
			}
			if _, err = f.s.NextIgnoreVerificationPage(f.ctx, l); err != domain.ErrConflict {
				t.Fatal("old mode admitted family", err)
			}
			if err = f.s.CommitFamilyIgnoreVerificationPage(f.ctx, l, p.Token, nil); err != domain.ErrConflict {
				t.Fatal("truncated page accepted", err)
			}
			err = nil
			switch mode {
			case "changed":
				p.Proofs[0].RuleSHA256[0] ^= 1
			case "deadline":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_verifications SET deadline=deadline+interval '1 second' WHERE job_id=$1::uuid`, l.Job.ID)
			case "missing-checkpoint":
				_, err = f.s.Pool.Exec(f.ctx, `DELETE FROM job_ignore_legacy_baseline_verifications WHERE job_id=$1::uuid`, l.Job.ID)
			case "legacy-invalid":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_manifests SET invalidated=true WHERE job_id=$1::uuid`, l.Job.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.CommitFamilyIgnoreVerificationPage(f.ctx, l, p.Token, p.Proofs); err != domain.ErrInventoryInvalidated {
				t.Fatal("inconsistent verification accepted", err)
			}
			var sequence int64
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT sequence FROM job_ignore_verifications WHERE job_id=$1::uuid`, l.Job.ID).Scan(&sequence); err != nil || sequence != 0 {
				t.Fatal("failed page advanced", err)
			}
			if mode == "changed" {
				_, _, _, invalid := manifestCounts(t, f, l.Job.ID)
				if !invalid {
					t.Fatal("source invalidation not committed")
				}
			}
		})
	}
}
