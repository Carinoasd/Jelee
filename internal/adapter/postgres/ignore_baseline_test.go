package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func baselineComparisonFixture(t *testing.T, total, seen int) (jobFixture, domain.JobLease, domain.IgnoreDirectoryProof) {
	t.Helper()
	f, l, root := manifestFixture(t)
	_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO library_inventory_baseline(library_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision)
 SELECT $1::uuid,$2::uuid,'f-'||lpad(n::text,6,'0')||'.mkv',true,'video',7,1,inventory_generation,inventory_baseline_revision FROM libraries CROSS JOIN generate_series(1,$3::int)n WHERE id=$1::uuid`, f.registration.Library.ID, root.RootID, total)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO job_inventory(job_id,root_id,parent_path,path,kind,size,modified_unix_nano)
 SELECT $1::uuid,$2::uuid,'.','f-'||lpad(n::text,6,'0')||'.mkv','video',7,1 FROM generate_series(1,$3::int)n`, l.Job.ID, root.RootID, seen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE job_directories SET done=true WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
		t.Fatal(err)
	}
	return f, l, root
}

func decisionPage(page domain.IgnoreBaselinePage, outcome string) []domain.IgnoreBaselineDecision {
	var result []domain.IgnoreBaselineDecision
	for _, entry := range page.Unseen {
		d := domain.IgnoreBaselineDecision{RootID: entry.RootID, Path: entry.Path, Outcome: outcome}
		if outcome == domain.IgnoreBaselineExcluded {
			d.RuleDirectory = "."
			d.RuleLine = 1
			d.MatchedPath = d.Path
		}
		if outcome == domain.IgnoreBaselineUnknown {
			d.Reason = domain.IgnoreUnknownSource
		}
		result = append(result, d)
	}
	return result
}

func comparisonCounts(t *testing.T, f jobFixture, id string) (domain.IgnoreComparisonCounts, int64, bool) {
	t.Helper()
	var c domain.IgnoreComparisonCounts
	var sequence int64
	var complete bool
	err := f.s.Pool.QueryRow(f.ctx, `SELECT observed,missing,excluded,unknown,sequence,completed FROM job_ignore_comparisons WHERE job_id=$1::uuid`, id).Scan(&c.Observed, &c.Missing, &c.Excluded, &c.Unknown, &sequence, &complete)
	if err != nil {
		t.Fatal(err)
	}
	return c, sequence, complete
}

func TestIgnoreBaselineRawPagesReplayAndReclaim(t *testing.T) {
	f, l, _ := baselineComparisonFixture(t, 260, 128)
	if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	first, err := f.s.NextIgnoreBaselinePage(f.ctx, l)
	if err != nil || first.RawCount != 128 || len(first.Unseen) != 0 || first.End {
		t.Fatal("empty unseen page confused with EOF", err)
	}
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, l, first.Token, nil); err != nil {
		t.Fatal(err)
	}
	second, err := f.s.NextIgnoreBaselinePage(f.ctx, l)
	if err != nil || second.RawCount != 128 || len(second.Unseen) != 128 || second.Token.AfterPath != "f-000128.mkv" {
		t.Fatal("raw cursor failed to advance", err)
	}
	decisions := decisionPage(second, domain.IgnoreBaselineExcluded)
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, l, second.Token, decisions[:127]); err != domain.ErrConflict {
		t.Fatal("partial classification accepted", err)
	}
	counts, seq, _ := comparisonCounts(t, f, l.Job.ID)
	if counts.Observed != 128 || counts.Excluded != 0 || seq != 1 {
		t.Fatal("failed prefix left partial decisions")
	}
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, l, second.Token, decisions); err != nil {
		t.Fatal(err)
	}
	if err = f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	reclaimed := ignoreManufacturedLease(t, f, l.Job.ID)
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, l, second.Token, decisions); err != domain.ErrJobLeaseLost {
		t.Fatal("stale owner replay accepted", err)
	}
	if err = f.s.BeginIgnoreBaselineComparison(f.ctx, reclaimed); err != nil {
		t.Fatal("begin replay", err)
	}
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, reclaimed, second.Token, decisions); err != nil {
		t.Fatal("exact durable replay", err)
	}
	decisions[0].RuleLine++
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, reclaimed, second.Token, decisions); err != domain.ErrConflict {
		t.Fatal("changed replay accepted", err)
	}
	last, err := f.s.NextIgnoreBaselinePage(f.ctx, reclaimed)
	if err != nil || last.RawCount != 4 || len(last.Unseen) != 4 {
		t.Fatal("last prefix", err)
	}
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, reclaimed, last.Token, decisionPage(last, domain.IgnoreBaselineUnknown)); err != nil {
		t.Fatal(err)
	}
	eof, err := f.s.NextIgnoreBaselinePage(f.ctx, reclaimed)
	if err != nil || !eof.End || eof.Complete {
		t.Fatal("EOF not explicit", err)
	}
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, reclaimed, eof.Token, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, reclaimed, eof.Token, nil); err != nil {
		t.Fatal("EOF replay", err)
	}
	counts, seq, complete := comparisonCounts(t, f, l.Job.ID)
	if counts != (domain.IgnoreComparisonCounts{Observed: 128, Excluded: 128, Unknown: 4}) || seq != 4 || !complete {
		t.Fatal("durable counts", counts, seq, complete)
	}
	if err = f.s.FinishJob(f.ctx, reclaimed, domain.JobSucceeded, ""); err != domain.ErrIgnoreUnavailable {
		t.Fatal("classification enabled execution", err)
	}
}

func TestIgnoreBaselineFreezesInventoryAndHistoryCanDelete(t *testing.T) {
	f, l, root := baselineComparisonFixture(t, 1, 1)
	if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE job_inventory SET size=size+1 WHERE job_id=$1::uuid`,
		`DELETE FROM job_inventory WHERE job_id=$1::uuid`,
		`INSERT INTO job_inventory(job_id,root_id,parent_path,path,kind,size,modified_unix_nano) SELECT $1::uuid,root_id,'.','new','video',1,1 FROM job_inventory WHERE job_id=$1::uuid`,
		`UPDATE job_directories SET skipped=1 WHERE job_id=$1::uuid`,
		`DELETE FROM job_directories WHERE job_id=$1::uuid`,
	} {
		if _, err := f.s.Pool.Exec(f.ctx, query, l.Job.ID); err == nil {
			t.Fatal("frozen inventory mutated")
		}
	}
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{manifestChild(root, "extra-source")}); err != nil {
		t.Fatal("classification cannot discover new proof", err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal("history cascade blocked", err)
	}
	var retained int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("history deleted baseline", err)
	}
}

func TestIgnoreBaselineBeginCoverageAndScope(t *testing.T) {
	for _, mode := range []string{"pending", "skipped", "no-root-proof", "scope-reset"} {
		t.Run(mode, func(t *testing.T) {
			f, l, _ := baselineComparisonFixture(t, 3, 0)
			var query string
			switch mode {
			case "pending":
				query = `UPDATE job_directories SET done=false WHERE job_id=$1::uuid`
			case "skipped":
				query = `UPDATE job_directories SET skipped=1 WHERE job_id=$1::uuid`
			case "no-root-proof":
				query = `DELETE FROM job_ignore_proofs WHERE job_id=$1::uuid`
			case "scope-reset":
				query = `UPDATE library_inventory_baseline SET inventory_generation=inventory_generation+1 WHERE library_id=(SELECT library_id FROM jobs WHERE id=$1::uuid)`
			}
			if _, err := f.s.Pool.Exec(f.ctx, query, l.Job.ID); err != nil {
				t.Fatal(err)
			}
			err := f.s.BeginIgnoreBaselineComparison(f.ctx, l)
			if mode == "scope-reset" {
				if err != nil {
					t.Fatal(err)
				}
				page, err := f.s.NextIgnoreBaselinePage(f.ctx, l)
				if err != nil || !page.Complete || !page.End || len(page.Unseen) != 0 {
					t.Fatal("incomparable old scope classified as missing", err)
				}
			} else if err != domain.ErrConflict {
				t.Fatal("unproven completion", mode, err)
			}
		})
	}
}

func TestIgnoreBaselineFencesAndLateExpiry(t *testing.T) {
	for _, mode := range []string{"revision", "epoch", "cancelled", "late"} {
		t.Run(mode, func(t *testing.T) {
			f, l, _ := baselineComparisonFixture(t, 1, 0)
			if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			page, err := f.s.NextIgnoreBaselinePage(f.ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			want := domain.ErrInventoryInvalidated
			switch mode {
			case "revision":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid`, f.registration.Library.ID)
			case "epoch":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=path||'-changed' WHERE id=$1::uuid`, f.registration.RootID)
			case "cancelled":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l.Job.ID)
				want = context.Canceled
			case "late":
				_, err = f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE comparison_entered; CREATE FUNCTION hold_comparison() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('comparison_entered'); PERFORM pg_sleep(0.2); RETURN NEW; END $$; CREATE TRIGGER hold_comparison BEFORE INSERT ON job_ignore_comparison_pages FOR EACH ROW EXECUTE FUNCTION hold_comparison()`)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '120 milliseconds' WHERE id=$1::uuid`, l.Job.ID)
				want = domain.ErrJobLeaseLost
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.CommitIgnoreBaselinePage(f.ctx, l, page.Token, decisionPage(page, domain.IgnoreBaselineMissing)); !errors.Is(err, want) {
				t.Fatal(mode, err)
			}
			if mode == "late" {
				var entered bool
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM comparison_entered`).Scan(&entered); err != nil || !entered {
					t.Fatal("late test did not reach receipt insert", err)
				}
			}
			counts, seq, _ := comparisonCounts(t, f, l.Job.ID)
			if counts != (domain.IgnoreComparisonCounts{}) || seq != 0 {
				t.Fatal("failed commit mutated progress")
			}
		})
	}
}

func TestIgnoreBaselinePrefixPlanBoundsSeenRows(t *testing.T) {
	f, l, _ := baselineComparisonFixture(t, 10000, 9900)
	if _, err := f.s.Pool.Exec(f.ctx, `ANALYZE library_inventory_baseline; ANALYZE job_inventory`); err != nil {
		t.Fatal(err)
	}
	plan := nfoWorkerPlan(t, f, ignoreBaselinePageSQL, f.registration.Library.ID, l.Job.ID, "", "")
	baseline, inventory := float64(0), float64(0)
	walkNFOWorkerPlan(plan, func(p nfoWorkerPlanNode) {
		if p.Relation == "library_inventory_baseline" {
			baseline += (p.Rows + p.Removed) * p.Loops
		}
		if p.Relation == "job_inventory" {
			inventory += (p.Rows + p.Removed) * p.Loops
		}
	})
	if baseline > 128 || inventory > 128 || plan.Rows != 128 {
		t.Fatalf("page scanned suffix: baseline=%g inventory=%g result=%g", baseline, inventory, plan.Rows)
	}
	t.Logf("10,000 baseline / 9,900 current: visited %g baseline and %g current rows for raw page 128", baseline, inventory)
}

func TestIgnoreBaselineClassificationNeedsActualProofChain(t *testing.T) {
	for _, mode := range []string{"missing-proof", "absent-parent", "excluded-ancestor", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f, l, root := baselineComparisonFixture(t, 1, 0)
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET path='gone/child.mkv' WHERE library_id=$1::uuid`, f.registration.Library.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "absent-parent" {
				p := manifestChild(root, "gone")
				p.MissingDirectory = true
				p.Identity = [32]byte{}
				if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{p}); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			page, err := f.s.NextIgnoreBaselinePage(f.ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			outcome := domain.IgnoreBaselineMissing
			if mode == "excluded-ancestor" {
				outcome = domain.IgnoreBaselineExcluded
			}
			if mode == "unknown" {
				outcome = domain.IgnoreBaselineUnknown
			}
			decisions := decisionPage(page, outcome)
			if mode == "excluded-ancestor" {
				decisions[0].MatchedPath = "gone"
			}
			err = f.s.CommitIgnoreBaselinePage(f.ctx, l, page.Token, decisions)
			if mode == "missing-proof" {
				if err != domain.ErrConflict {
					t.Fatal("missing database row treated as absence", err)
				}
			} else if err != nil {
				t.Fatal(mode, err)
			}
		})
	}
}

func TestIgnoreBaselineRevisionAndMigrationGuards(t *testing.T) {
	f := newJobFixture(t)
	f.complete(t, "revision-one", []string{"retained.mkv"}, 0)
	var revision, observed int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT l.inventory_baseline_revision,b.observed_revision FROM libraries l JOIN library_inventory_baseline b ON b.library_id=l.id WHERE l.id=$1::uuid`, f.registration.Library.ID).Scan(&revision, &observed); err != nil || revision <= 1 || observed != revision {
		t.Fatal("ordinary publication did not bind observed revision", err)
	}
	nfoMigrateVersion(t, f, "down", 9)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	j := ignoreSubmit(t, f, "migration-comparison")
	l := ignoreManufacturedLease(t, f, j.ID)
	root := domain.IgnoreDirectoryProof{RootID: f.registration.RootID, Directory: ".", Identity: [32]byte{1}}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_directories SET done=true WHERE job_id=$1::uuid`, j.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	nfoMigrationDenied(t, f, "000010_ignore_baseline.down.sql")
	if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	nfoMigrationDenied(t, f, "000010_ignore_baseline.down.sql")
}
