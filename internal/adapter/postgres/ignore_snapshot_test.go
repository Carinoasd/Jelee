package postgres

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestIgnoreSnapshotFamilyPreparesBeforeSealAndPreservesHistory(t *testing.T) {
	f, l := familyComparisonFixture(t)
	classifyFamilyForPublication(t, f, l, familyExcluded, false)
	before := imageBaseline(t, f)
	ready, err := f.s.PrepareInventoryPublication(f.ctx, l)
	if err != nil || ready {
		t.Fatal("ignored snapshot did not stage a bounded first batch", ready, err)
	}
	if imageBaseline(t, f) != before {
		t.Fatal("partial ignored snapshot became visible")
	}
	prepareSnapshot(t, f, l)
	if imageBaseline(t, f) != before {
		t.Fatal("ready ignored snapshot became visible without publication")
	}
	if err = f.s.FinishFamilyIgnoreJob(f.ctx, l); err == nil {
		t.Fatal("unsealed ignored snapshot published")
	}
	if imageBaseline(t, f) != before {
		t.Fatal("unsealed publication changed history")
	}
	finishFamilyVerification(t, f, l)
	if err = f.s.SealFamilyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err = f.s.FinishFamilyIgnoreJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	var historical, current int
	var active int64
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FILTER(WHERE b.observed_revision=1),count(*) FILTER(WHERE b.observed_revision=l.inventory_baseline_revision),max(l.active_inventory_snapshot) FROM library_inventory_baseline b JOIN libraries l ON l.id=b.library_id WHERE l.id=$1::uuid`, f.registration.Library.ID).Scan(&historical, &current, &active); err != nil || historical != 130 || current != 1 || active == 0 {
		t.Fatal("history or pointer was not preserved", historical, current, active, err)
	}
	if job := f.get(t, l.Job.ID); job.State != domain.JobSucceeded || job.ReviewRequired {
		t.Fatal("ignored snapshot terminal state", job)
	}
}

func TestIgnoreSnapshotCustomHistoryAndZeroObserved(t *testing.T) {
	for _, seen := range []int{0, 130} {
		t.Run(strconv.Itoa(seen), func(t *testing.T) {
			f, l, _ := baselineComparisonFixture(t, 260, seen)
			// This fixture inserts inventory directly; make its durable counters
			// match a completed scanner before comparison freezes the rows.
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET files=$2::bigint,bytes=$2::bigint*7 WHERE id=$1::uuid`, l.Job.ID, seen); err != nil {
				t.Fatal(err)
			}
			classifyForPublication(t, f, l, func(*domain.IgnoreBaselineDecision) {}, false)
			before := imageBaseline(t, f)
			prepareSnapshot(t, f, l)
			if imageBaseline(t, f) != before {
				t.Fatal("custom snapshot visible before verification")
			}
			if err := f.s.BeginIgnoreVerification(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			finishVerification(t, f, l)
			if err := f.s.SealIgnoreVerification(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			if err := f.s.FinishIgnoreJob(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			var history, current int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FILTER(WHERE observed_revision=1),count(*) FILTER(WHERE observed_revision=2) FROM library_inventory_baseline WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&history, &current); err != nil || history != 260-seen || current != seen {
				t.Fatal("custom history lost provenance", history, current, err)
			}
		})
	}
}

func TestIgnoreSnapshotReclaimAndInvalidation(t *testing.T) {
	for _, mode := range []string{"reclaim", "cancel", "roots", "baseline", "mode"} {
		t.Run(mode, func(t *testing.T) {
			f, l := familyComparisonFixture(t)
			classifyFamilyForPublication(t, f, l, familyExcluded, false)
			if ready, err := f.s.PrepareInventoryPublication(f.ctx, l); err != nil || ready {
				t.Fatal(ready, err)
			}
			before := imageBaseline(t, f)
			want := domain.ErrInventoryInvalidated
			switch mode {
			case "reclaim":
				if err := f.s.ReleaseJob(f.ctx, l); err != nil {
					t.Fatal(err)
				}
				replacement := ignoreManufacturedLease(t, f, l.Job.ID)
				if _, err := f.s.PrepareInventoryPublication(f.ctx, l); !errors.Is(err, domain.ErrJobLeaseLost) {
					t.Fatal("previous owner wrote preparation", err)
				}
				prepareSnapshot(t, f, replacement)
				finishFamilyVerification(t, f, replacement)
				if err := f.s.SealFamilyIgnoreVerification(f.ctx, replacement); err != nil {
					t.Fatal(err)
				}
				if err := f.s.FinishFamilyIgnoreJob(f.ctx, replacement); err != nil || snapshotCount(t, f) != 131 {
					t.Fatal("replacement did not resume snapshot", err)
				}
				return
			case "cancel":
				want = context.Canceled
				if _, err := f.s.CancelJob(f.ctx, f.a, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			case "roots", "baseline":
				column := "inventory_generation"
				if mode == "baseline" {
					column = "inventory_baseline_revision"
				}
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET `+column+`=`+column+`+1 WHERE id=$1::uuid`, f.registration.Library.ID); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE inventory_snapshot_preparations SET publication_mode='jeleeignore' WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.PrepareInventoryPublication(f.ctx, l); !errors.Is(err, want) {
				t.Fatal("invalid preparation accepted", err)
			}
			if imageBaseline(t, f) != before {
				t.Fatal("invalid preparation changed visible history")
			}
		})
	}
}

func TestIgnoreSnapshotReviewAndLimit(t *testing.T) {
	for _, mode := range []string{"unknown", "missing", "limit", "reset"} {
		t.Run(mode, func(t *testing.T) {
			f, l := familyComparisonFixture(t)
			if mode == "reset" {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET inventory_generation=inventory_generation+1`); err != nil {
					t.Fatal(err)
				}
			}
			classifyFamilyForPublication(t, f, l, func(e *domain.FamilyBaselineEvaluation) {
				switch mode {
				case "unknown":
					*e = domain.FamilyBaselineEvaluation{Decision: domain.FamilyIgnoreBaselineDecision{RootID: e.Decision.RootID, Path: e.Decision.Path, Outcome: domain.IgnoreBaselineUnknown, Reason: domain.IgnoreUnknownSource}}
				case "missing":
				default:
					familyExcluded(e)
				}
			}, false)
			if mode == "limit" {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET max_entries=100 WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.PrepareInventoryPublication(f.ctx, l); !errors.Is(err, domain.ErrScanLimit) {
					t.Fatal("excluded history omitted from limit", err)
				}
			} else {
				prepareSnapshot(t, f, l)
			}
			var preparations int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM inventory_snapshot_preparations`).Scan(&preparations); err != nil {
				t.Fatal(err)
			}
			if mode == "reset" {
				if preparations != 1 {
					t.Fatal("new scope did not prepare current observations")
				}
				finishFamilyVerification(t, f, l)
				if err := f.s.SealFamilyIgnoreVerification(f.ctx, l); err != nil {
					t.Fatal(err)
				}
				if err := f.s.FinishFamilyIgnoreJob(f.ctx, l); err != nil || snapshotCount(t, f) != 1 {
					t.Fatal("new scope retained excluded history", err)
				}
			} else if preparations != 0 {
				t.Fatal("review/limit staged a publishable snapshot")
			}
		})
	}
}

func TestIgnoreSnapshotSparseHistoryPages(t *testing.T) {
	f, l, _ := baselineComparisonFixture(t, 260, 1)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET files=1,bytes=7,missing_count_limit=500000,missing_percent_limit=100 WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	classifyForPublication(t, f, l, func(d *domain.IgnoreBaselineDecision) {
		if d.Path != "f-000260.mkv" {
			missingDecision(d)
		}
	}, false)
	for _, want := range []struct {
		path   string
		copied int
		ready  bool
	}{{"", 0, false}, {"f-000129.mkv", 0, false}, {"f-000257.mkv", 0, false}, {"f-000260.mkv", 1, true}} {
		ready, err := f.s.PrepareInventoryPublication(f.ctx, l)
		if err != nil || ready != want.ready {
			t.Fatal("sparse history readiness", ready, err)
		}
		var path string
		var copied int
		if err = f.s.Pool.QueryRow(f.ctx, `SELECT excluded_after_path,excluded_copied FROM inventory_snapshot_preparations WHERE job_id=$1::uuid`, l.Job.ID).Scan(&path, &copied); err != nil || path != want.path || copied != want.copied {
			t.Fatal("sparse exclusions exceeded raw page bound", path, copied, err)
		}
	}
}

func TestIgnoreSnapshotLateSealAndLeaseRollback(t *testing.T) {
	for _, mode := range []string{"seal", "lease"} {
		t.Run(mode, func(t *testing.T) {
			f, l := familyComparisonFixture(t)
			classifyFamilyForPublication(t, f, l, familyExcluded, false)
			prepareSnapshot(t, f, l)
			finishFamilyVerification(t, f, l)
			if err := f.s.SealFamilyIgnoreVerification(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE snapshot_publish_entered; CREATE FUNCTION snapshot_publish_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('snapshot_publish_entered'); PERFORM pg_sleep(0.3); RETURN NEW; END $$; CREATE TRIGGER snapshot_publish_delay BEFORE UPDATE OF state ON jobs FOR EACH ROW EXECUTE FUNCTION snapshot_publish_delay()`); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE job_ignore_verifications SET sealed_until=clock_timestamp()+interval '150 milliseconds' WHERE job_id=$1::uuid`
			if mode == "lease" {
				query = `UPDATE jobs SET lease_until=clock_timestamp()+interval '150 milliseconds' WHERE id=$1::uuid`
			}
			if _, err := f.s.Pool.Exec(f.ctx, query, l.Job.ID); err != nil {
				t.Fatal(err)
			}
			before := ignoreSnapshot(t, f)
			if err := f.s.FinishFamilyIgnoreJob(f.ctx, l); err == nil || ignoreSnapshot(t, f) != before {
				t.Fatal("expired publication changed durable state", err)
			}
			var entered bool
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM snapshot_publish_entered`).Scan(&entered); err != nil || !entered {
				t.Fatal("late guard was not exercised", err)
			}
		})
	}
}

func TestIgnoreSnapshotMigration(t *testing.T) {
	t.Run("plain-preparation-round-trip", func(t *testing.T) {
		f, l := snapshotFixture(t, 130, legacyMigrationAt44)
		if _, err := f.s.PrepareInventoryPublication(f.ctx, l); err != nil {
			t.Fatal(err)
		}
		nfoMigrateVersion(t, f, "down", 43)
		nfoMigrateVersion(t, f, "up", SchemaVersion)
		prepareSnapshot(t, f, l)
		if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil || snapshotCount(t, f) != 130 {
			t.Fatal("plain snapshot could not resume after upgrade", err)
		}
	})
	t.Run("retained-ignore-denies-downgrade", func(t *testing.T) {
		f, l := familyComparisonFixture(t, legacyMigrationAt44)
		classifyFamilyForPublication(t, f, l, familyExcluded, false)
		if _, err := f.s.PrepareInventoryPublication(f.ctx, l); err != nil {
			t.Fatal(err)
		}
		nfoMigrationDenied(t, f, "000044_ignore_snapshots.down.sql")
		prepareSnapshot(t, f, l)
	})
}
