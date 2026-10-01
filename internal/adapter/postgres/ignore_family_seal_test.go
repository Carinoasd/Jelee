package postgres

import (
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func familySealFixture(t *testing.T) (jobFixture, domain.JobLease) {
	t.Helper()
	f, l := familyVerificationFixture(t)
	if err := f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	for {
		p, err := f.s.NextFamilyIgnoreVerificationPage(f.ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		if p.Complete {
			break
		}
		if err = f.s.CommitFamilyIgnoreVerificationPage(f.ctx, l, p.Token, p.Proofs); err != nil {
			t.Fatal(err)
		}
	}
	for {
		p, err := f.s.NextLegacyIgnoreVerificationPage(f.ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		if p.Complete {
			break
		}
		if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, p.Token, p.Observations); err != nil {
			t.Fatal(err)
		}
	}
	for {
		p, err := f.s.NextLegacyIgnoreBaselineVerificationPage(f.ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		if p.Complete {
			break
		}
		if err = f.s.CommitLegacyIgnoreBaselineVerificationPage(f.ctx, l, p.Token, p.Observations); err != nil {
			t.Fatal(err)
		}
	}
	return f, l
}

func TestFamilySealRetryAndExpiry(t *testing.T) {
	f, l := familySealFixture(t)
	if err := f.s.SealFamilyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	var first, last, deadline time.Time
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT sealed_until,deadline FROM job_ignore_verifications WHERE job_id=$1::uuid`, l.Job.ID).Scan(&first, &deadline); err != nil {
		t.Fatal(err)
	}
	if first.After(deadline) || first.After(time.Now().Add(31*time.Second)) {
		t.Fatal("seal exceeded deadline")
	}
	if err := f.s.SealFamilyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT sealed_until FROM job_ignore_verifications WHERE job_id=$1::uuid`, l.Job.ID).Scan(&last); err != nil || !last.Equal(first) {
		t.Fatal("seal renewed", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_verifications SET sealed_until=clock_timestamp()-interval '1 second' WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SealFamilyIgnoreVerification(f.ctx, l); err != domain.ErrInventoryInvalidated {
		t.Fatal("expired seal renewed", err)
	}
}

func TestFamilySealCountsAndLateDeadline(t *testing.T) {
	for _, mode := range []string{"custom-count", "source-count", "baseline-count", "late"} {
		t.Run(mode, func(t *testing.T) {
			f, l := familySealFixture(t)
			want := domain.ErrConflict
			var err error
			switch mode {
			case "custom-count":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_verifications SET verified_rows=verified_rows+1 WHERE job_id=$1::uuid`, l.Job.ID)
			case "source-count":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_verifications SET verified_queries=verified_queries+1 WHERE job_id=$1::uuid`, l.Job.ID)
			case "baseline-count":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_baseline_verifications SET verified_queries=verified_queries+1 WHERE job_id=$1::uuid`, l.Job.ID)
			case "late":
				_, err = f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE family_seal_entered; CREATE FUNCTION family_seal_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('family_seal_entered'); PERFORM pg_sleep(0.3); RETURN NEW; END $$; CREATE TRIGGER family_seal_delay BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION family_seal_delay()`)
				if err != nil {
					t.Fatal(err)
				}
				var deadline time.Time
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT clock_timestamp()+interval '150 milliseconds'`).Scan(&deadline); err != nil {
					t.Fatal(err)
				}
				for _, table := range []string{"job_ignore_verifications", "job_ignore_legacy_verifications", "job_ignore_legacy_baseline_verifications"} {
					if _, err = f.s.Pool.Exec(f.ctx, `UPDATE `+table+` SET deadline=$2 WHERE job_id=$1::uuid`, l.Job.ID, deadline); err != nil {
						t.Fatal(err)
					}
				}
				want = domain.ErrInventoryInvalidated
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.SealFamilyIgnoreVerification(f.ctx, l); err != want {
				t.Fatal("invalid seal accepted", mode, err)
			}
			var absent bool
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT sealed_until IS NULL FROM job_ignore_verifications WHERE job_id=$1::uuid`, l.Job.ID).Scan(&absent); err != nil || !absent {
				t.Fatal("failed seal persisted", err)
			}
			if mode == "late" {
				var entered bool
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM family_seal_entered`).Scan(&entered); err != nil || !entered {
					t.Fatal("late write was not exercised", err)
				}
			}
		})
	}
}

func TestFamilyBeginExpiredLegacyRollsBackCoordinator(t *testing.T) {
	f, l := familyVerificationFixture(t)
	if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginLegacyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_verifications SET deadline='2000-01-01' WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != domain.ErrInventoryInvalidated {
		t.Fatal("expired independent deadline accepted", err)
	}
	var frozen bool
	var checkpoints int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT frozen,(SELECT count(*) FROM job_ignore_verifications WHERE job_id=$1::uuid)+(SELECT count(*) FROM job_ignore_legacy_baseline_verifications WHERE job_id=$1::uuid) FROM job_ignore_manifests WHERE job_id=$1::uuid`, l.Job.ID).Scan(&frozen, &checkpoints); err != nil || frozen || checkpoints != 0 {
		t.Fatal("partial coordinator committed", err)
	}
}

func TestFamilyVerificationRejectsUnknownComparison(t *testing.T) {
	f, l := familyComparisonFixture(t)
	if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	for {
		p, err := f.s.NextFamilyIgnoreBaselinePage(f.ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		if p.Complete {
			break
		}
		var e []domain.FamilyBaselineEvaluation
		for _, candidate := range p.Unseen {
			e = append(e, domain.FamilyBaselineEvaluation{Decision: domain.FamilyIgnoreBaselineDecision{RootID: candidate.RootID, Path: candidate.Path, Outcome: domain.IgnoreBaselineUnknown, Reason: domain.IgnoreUnknownSource}})
		}
		if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.BeginFamilyIgnoreVerification(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("unknown comparison verified", err)
	}
}
