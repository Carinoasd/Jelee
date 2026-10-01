package postgres

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func classifyForPublication(t *testing.T, f jobFixture, l domain.JobLease, choose func(*domain.IgnoreBaselineDecision), seal bool) {
	t.Helper()
	if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	for {
		p, err := f.s.NextIgnoreBaselinePage(f.ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		if p.Complete {
			break
		}
		d := decisionPage(p, domain.IgnoreBaselineExcluded)
		for i := range d {
			choose(&d[i])
		}
		if err = f.s.CommitIgnoreBaselinePage(f.ctx, l, p.Token, d); err != nil {
			t.Fatal(err)
		}
	}
	if seal {
		if err := f.s.BeginIgnoreVerification(f.ctx, l); err != nil {
			t.Fatal(err)
		}
		finishVerification(t, f, l)
		if err := f.s.SealIgnoreVerification(f.ctx, l); err != nil {
			t.Fatal(err)
		}
	}
}

func missingDecision(d *domain.IgnoreBaselineDecision) {
	*d = domain.IgnoreBaselineDecision{RootID: d.RootID, Path: d.Path, Outcome: domain.IgnoreBaselineMissing}
}

func TestIgnorePublicationProtectedMergeAndImageTransitions(t *testing.T) {
	f, l, _ := baselineComparisonFixture(t, 5, 3)
	_, err := f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET kind='image' WHERE path IN ('f-000001.mkv','f-000002.mkv','f-000004.mkv','f-000005.mkv'); UPDATE job_inventory SET kind='image',size=9 WHERE path IN ('f-000002.mkv','f-000003.mkv')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO job_inventory(job_id,root_id,parent_path,path,kind,size,modified_unix_nano) VALUES($1::uuid,$2::uuid,'.','new.jpg','image',10,1)`, l.Job.ID, f.registration.RootID)
	if err != nil {
		t.Fatal(err)
	}
	classifyForPublication(t, f, l, func(d *domain.IgnoreBaselineDecision) {
		if d.Path == "f-000005.mkv" {
			missingDecision(d)
		}
	}, true)
	if err = f.s.FinishIgnoreJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	j := f.get(t, l.Job.ID)
	if j.State != domain.JobSucceeded || j.ReviewRequired || j.Missing != 1 {
		t.Fatal("general missing included excluded/image transition", j)
	}
	p, err := f.s.GetImageJobSummary(f.ctx, f.a, l.Job.ID)
	if err != nil || p.ImageProgress != (domain.ImageProgress{Added: 2, Changed: 1, Missing: 2, ComparisonComplete: true}) {
		t.Fatal("image transition/exclusion counts", p, err)
	}
	var total, protected, current int64
	err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE path='f-000004.mkv' AND kind='image' AND size=7 AND observed_revision=1),count(*) FILTER(WHERE observed_revision=l.inventory_baseline_revision) FROM library_inventory_baseline b JOIN libraries l ON l.id=b.library_id WHERE b.library_id=$1::uuid`, f.registration.Library.ID).Scan(&total, &protected, &current)
	if err != nil || total != 5 || protected != 1 || current != 4 {
		t.Fatal("protected provenance overwritten", total, protected, current, err)
	}
	before := imageBaseline(t, f)
	if _, err = f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, l.Job.ID); err != nil || imageBaseline(t, f) != before {
		t.Fatal("history deletion changed protected baseline", err)
	}
	// Turning ignore off re-evaluates the formerly protected path normally.
	off := f.complete(t, "off-after-ignore", []string{"f-000001.mkv", "f-000002.mkv", "f-000003.mkv", "new.jpg"}, 0)
	if off.Missing != 1 || off.ReviewRequired {
		t.Fatal("old exclusion leaked into off scan", off)
	}
}

func TestIgnorePublicationReviewDoesNotDiluteMissing(t *testing.T) {
	f, l, _ := baselineComparisonFixture(t, 100, 1)
	classifyForPublication(t, f, l, func(d *domain.IgnoreBaselineDecision) {
		if d.Path == "f-000002.mkv" {
			missingDecision(d)
		}
	}, true)
	before := imageBaseline(t, f)
	if err := f.s.FinishIgnoreJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	j := f.get(t, l.Job.ID)
	if !j.ReviewRequired || j.Missing != 1 || imageBaseline(t, f) != before {
		t.Fatal("98 excluded paths diluted 1/2 missing threshold", j)
	}
}

func TestIgnorePublicationUnknownAndScopeReset(t *testing.T) {
	for _, mode := range []string{"unknown", "reset"} {
		t.Run(mode, func(t *testing.T) {
			f, l, _ := baselineComparisonFixture(t, 3, 1)
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_inventory SET kind='image' WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "reset" {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET inventory_generation=inventory_generation+1 WHERE library_id=$1::uuid`, f.registration.Library.ID); err != nil {
					t.Fatal(err)
				}
			}
			classifyForPublication(t, f, l, func(d *domain.IgnoreBaselineDecision) {
				*d = domain.IgnoreBaselineDecision{RootID: d.RootID, Path: d.Path, Outcome: domain.IgnoreBaselineUnknown, Reason: domain.IgnoreUnknownSource}
			}, mode == "reset")
			before := imageBaseline(t, f)
			if err := f.s.FinishIgnoreJob(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			j := f.get(t, l.Job.ID)
			if j.Missing != 0 || j.ReviewRequired != (mode == "unknown") {
				t.Fatal("unknown/reset conflated", j)
			}
			p, err := f.s.GetImageJobSummary(f.ctx, f.a, l.Job.ID)
			if err != nil || p.ImageProgress != (domain.ImageProgress{Uncompared: 1}) {
				t.Fatal("uncertain image compared", p, err)
			}
			if mode == "unknown" && imageBaseline(t, f) != before {
				t.Fatal("unknown overwrote baseline")
			}
			if mode == "reset" {
				var count int
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&count); err != nil || count != 1 {
					t.Fatal("reset retained old scope", err)
				}
			}
		})
	}
}

func TestIgnorePublicationSealAndLimitFailuresAreAtomic(t *testing.T) {
	for _, mode := range []string{"unsealed", "expired", "late", "limit"} {
		t.Run(mode, func(t *testing.T) {
			total := 2
			if mode == "limit" {
				total = 101
			}
			f, l, _ := baselineComparisonFixture(t, total, 1)
			classifyForPublication(t, f, l, func(*domain.IgnoreBaselineDecision) {}, mode != "unsealed")
			var err error
			if mode == "expired" {
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_verifications SET sealed_until=clock_timestamp()-interval '1 second' WHERE job_id=$1::uuid`, l.Job.ID)
			}
			if mode == "limit" {
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET max_entries=100 WHERE id=$1::uuid`, l.Job.ID)
			}
			if mode == "late" {
				_, err = f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE publication_entered; CREATE FUNCTION hold_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('publication_entered'); PERFORM pg_sleep(0.2); RETURN NEW; END $$; CREATE TRIGGER hold_publication BEFORE UPDATE OF state ON jobs FOR EACH ROW EXECUTE FUNCTION hold_publication()`)
				if err == nil {
					_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_verifications SET sealed_until=clock_timestamp()+interval '120 milliseconds' WHERE job_id=$1::uuid`, l.Job.ID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := ignoreSnapshot(t, f)
			if err = f.s.FinishIgnoreJob(f.ctx, l); err == nil || ignoreSnapshot(t, f) != before {
				t.Fatal("rejected publication left changes", mode, err)
			}
			if mode == "limit" && err != domain.ErrScanLimit {
				t.Fatal("merged limit not checked", err)
			}
			if mode == "late" {
				var entered bool
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM publication_entered`).Scan(&entered); err != nil || !entered {
					t.Fatal("late test did not reach terminal write", err)
				}
			}
		})
	}
}
