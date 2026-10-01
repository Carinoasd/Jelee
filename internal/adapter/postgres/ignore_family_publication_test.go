package postgres

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func classifyFamilyForPublication(t *testing.T, f jobFixture, l domain.JobLease, choose func(*domain.FamilyBaselineEvaluation), seal bool) {
	t.Helper()
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
		e := familyPageEvaluations(t, f, l, p)
		for i := range e {
			choose(&e[i])
		}
		if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, e); err != nil {
			t.Fatal(err)
		}
	}
	if seal {
		finishFamilyVerification(t, f, l)
		if err := f.s.SealFamilyIgnoreVerification(f.ctx, l); err != nil {
			t.Fatal(err)
		}
	}
}

func familyExcluded(e *domain.FamilyBaselineEvaluation) {
	e.Decision.Outcome = domain.IgnoreBaselineExcluded
	e.Decision.Family = domain.IgnoreFamilyLegacy
	e.Decision.Reason = domain.IgnoreReasonBlank
	e.Decision.RuleDirectory = "."
	e.Decision.MatchedPath = e.Decision.Path
}

func TestFamilyPublicationPreservesExcludedAndImageCounts(t *testing.T) {
	f, l := familyComparisonFixture(t)
	// Keep one missing image, one excluded historical image, and enough
	// observed rows that a single missing file is below the review threshold.
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM library_inventory_baseline WHERE library_id=$1::uuid AND path>'f-000004.mkv'`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET kind='image' WHERE library_id=$1::uuid AND path IN ('f-000003.mkv','f-000004.mkv')`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	// The scan was already drained by the fixture, but no comparison has
	// frozen it yet; these rows model files included in that completed scan.
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_inventory(job_id,root_id,parent_path,path,kind,size,modified_unix_nano) SELECT $1::uuid,$2::uuid,'.','f-'||lpad(n::text,6,'0')||'.mkv','video',7,1 FROM generate_series(1,2)n`, l.Job.ID, f.registration.RootID); err != nil {
		t.Fatal(err)
	}
	classifyFamilyForPublication(t, f, l, func(e *domain.FamilyBaselineEvaluation) {
		if e.Decision.Path == "f-000004.mkv" {
			familyExcluded(e)
		}
	}, true)
	if err := f.s.FinishIgnoreJob(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("old publisher admitted family", err)
	}
	if err := f.s.FinishFamilyIgnoreJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	j := f.get(t, l.Job.ID)
	if j.State != domain.JobSucceeded || j.ReviewRequired || j.Missing != 1 {
		t.Fatal("publication counts", j)
	}
	var total, protected, current int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE path='f-000004.mkv' AND kind='image' AND size=7 AND observed_revision=1),count(*) FILTER(WHERE observed_revision=l.inventory_baseline_revision) FROM library_inventory_baseline b JOIN libraries l ON l.id=b.library_id WHERE b.library_id=$1::uuid`, f.registration.Library.ID).Scan(&total, &protected, &current); err != nil || total != 4 || protected != 1 || current != 3 {
		t.Fatal("protected merge", total, protected, current, err)
	}
	var missing int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT missing FROM image_job_state WHERE job_id=$1::uuid`, l.Job.ID).Scan(&missing); err != nil || missing != 1 {
		t.Fatal("family image classification ignored", missing, err)
	}
}

func TestFamilyPublicationUnknownPreservesBaseline(t *testing.T) {
	f, l := familyComparisonFixture(t)
	classifyFamilyForPublication(t, f, l, func(e *domain.FamilyBaselineEvaluation) {
		*e = domain.FamilyBaselineEvaluation{Decision: domain.FamilyIgnoreBaselineDecision{RootID: e.Decision.RootID, Path: e.Decision.Path, Outcome: domain.IgnoreBaselineUnknown, Reason: domain.IgnoreUnknownSource}}
	}, false)
	before := imageBaseline(t, f)
	if err := f.s.FinishFamilyIgnoreJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	j := f.get(t, l.Job.ID)
	if !j.ReviewRequired || j.Missing != 0 || imageBaseline(t, f) != before {
		t.Fatal("unknown published missing baseline", j)
	}
}

func TestFamilyPublicationReviewResetAndMergedLimit(t *testing.T) {
	for _, mode := range []string{"threshold", "scope-reset", "merged-limit"} {
		t.Run(mode, func(t *testing.T) {
			f, l := familyComparisonFixture(t)
			if mode == "scope-reset" {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET inventory_generation=inventory_generation+1 WHERE library_id=$1::uuid`, f.registration.Library.ID); err != nil {
					t.Fatal(err)
				}
			}
			classifyFamilyForPublication(t, f, l, func(e *domain.FamilyBaselineEvaluation) {
				if mode == "threshold" && e.Decision.Path == "f-000001.mkv" {
					return
				}
				familyExcluded(e)
			}, true)
			if mode == "merged-limit" {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET max_entries=100 WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			}
			before := ignoreSnapshot(t, f)
			baseline := imageBaseline(t, f)
			err := f.s.FinishFamilyIgnoreJob(f.ctx, l)
			if mode == "merged-limit" {
				if err != domain.ErrScanLimit || ignoreSnapshot(t, f) != before {
					t.Fatal("protected rows omitted from limit", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			job := f.get(t, l.Job.ID)
			if mode == "threshold" {
				if !job.ReviewRequired || job.Missing != 1 || imageBaseline(t, f) != baseline {
					t.Fatal("exclusions diluted missing threshold", job)
				}
			} else {
				var count int
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&count); err != nil || count != 1 || job.Missing != 0 || job.ReviewRequired {
					t.Fatal("old scope retained or reported missing", count, err)
				}
			}
		})
	}
}

func TestFamilyPublicationSealFailuresAreAtomic(t *testing.T) {
	for _, mode := range []string{"unsealed", "source-count", "baseline-expired", "late"} {
		t.Run(mode, func(t *testing.T) {
			f, l := familyComparisonFixture(t)
			classifyFamilyForPublication(t, f, l, familyExcluded, mode != "unsealed")
			var err error
			switch mode {
			case "source-count":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_verifications SET verified_queries=verified_queries+1 WHERE job_id=$1::uuid`, l.Job.ID)
			case "baseline-expired":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_baseline_verifications SET deadline='2000-01-01' WHERE job_id=$1::uuid`, l.Job.ID)
			case "late":
				_, err = f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE family_publish_entered; CREATE FUNCTION family_publish_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('family_publish_entered'); PERFORM pg_sleep(0.3); RETURN NEW; END $$; CREATE TRIGGER family_publish_delay BEFORE UPDATE OF state ON jobs FOR EACH ROW EXECUTE FUNCTION family_publish_delay()`)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_verifications SET sealed_until=clock_timestamp()+interval '150 milliseconds' WHERE job_id=$1::uuid`, l.Job.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := ignoreSnapshot(t, f)
			if err = f.s.FinishFamilyIgnoreJob(f.ctx, l); err == nil || ignoreSnapshot(t, f) != before {
				t.Fatal("rejected publication changed data", mode, err)
			}
			if mode == "late" {
				var entered bool
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM family_publish_entered`).Scan(&entered); err != nil || !entered {
					t.Fatal("late publish not exercised", err)
				}
			}
		})
	}
}
