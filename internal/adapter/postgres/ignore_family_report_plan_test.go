package postgres

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestFamilyReportDeepPageUsesBoundedIndexes(t *testing.T) {
	f, l := familyComparisonFixture(t)
	// Synthetic rows are confined to the disposable integration schema. This
	// measures read pagination, not worker classification or publication.
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_family_exclusions(job_id,root_id,parent_path,path,kind,rule_directory,rule_line,matched_path,family,reason)
 SELECT $1::uuid,$2::uuid,'.','f-'||lpad(n::text,6,'0')||'.mkv','video','.',1,'f-'||lpad(n::text,6,'0')||'.mkv','jeleeignore','rule' FROM generate_series(1,10000)n`, l.Job.ID, f.registration.RootID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_family_decisions(job_id,root_id,path,outcome,rule_directory,rule_line,matched_path,reason,family)
 SELECT $1::uuid,$2::uuid,'f-'||lpad(n::text,6,'0')||'.mkv','included_missing','',0,'','','' FROM generate_series(1,10000)n`, l.Job.ID, f.registration.RootID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, "ANALYZE job_ignore_family_decisions; ANALYZE job_ignore_family_exclusions"); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"", "baseline", "scan"} {
		t.Run(source, func(t *testing.T) {
			plan := nfoWorkerPlan(t, f, familyIgnoreReportPageSQL, l.Job.ID, source, f.registration.RootID, "f-009000.mkv", 51)
			if plan.Rows != 51 {
				t.Fatal("wrong page size", plan.Rows)
			}
			visited := float64(0)
			walkNFOWorkerPlan(plan, func(p nfoWorkerPlanNode) {
				if p.Relation == "job_ignore_family_decisions" || p.Relation == "job_ignore_family_exclusions" {
					visited += (p.Rows + p.Removed) * p.Loops
					if p.Loops > 0 && p.Index == "" {
						t.Error("report scanned relation without cursor index", p.Type)
					}
				}
			})
			if visited > 102 {
				t.Fatal("deep page scanned preceding history", visited)
			}
			t.Logf("20,000 generated rows, source=%q: visited=%g for limit+1=51", source, visited)
		})
	}
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err = tx.Exec(f.ctx, "SET LOCAL plan_cache_mode=force_generic_plan"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, "PREPARE ignore_report_plan(uuid,text,text,text,int) AS "+familyIgnoreReportPageSQL); err != nil {
		t.Fatal(err)
	}
	defer tx.Exec(f.ctx, "DEALLOCATE ignore_report_plan")
	for _, source := range []string{"baseline", "scan"} {
		var raw []byte
		// Only fixture UUIDs and fixed source names are interpolated here.
		query := fmt.Sprintf("EXPLAIN(ANALYZE,FORMAT JSON,COSTS false) EXECUTE ignore_report_plan('%s','%s','%s','f-009000.mkv',51)", l.Job.ID, source, f.registration.RootID)
		if err = tx.QueryRow(f.ctx, query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var plans []struct{ Plan nfoWorkerPlanNode }
		if json.Unmarshal(raw, &plans) != nil || len(plans) != 1 {
			t.Fatal("invalid generic plan")
		}
		visited := float64(0)
		walkNFOWorkerPlan(plans[0].Plan, func(p nfoWorkerPlanNode) {
			if p.Relation == "job_ignore_family_decisions" || p.Relation == "job_ignore_family_exclusions" {
				visited += (p.Rows + p.Removed) * p.Loops
			}
		})
		if visited > 102 || plans[0].Plan.Rows != 51 {
			t.Errorf("generic %s plan traversed history: visited=%g rows=%g", source, visited, plans[0].Plan.Rows)
		}
	}
}
