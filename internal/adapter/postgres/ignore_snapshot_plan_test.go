package postgres

import "testing"

func TestIgnoreSnapshotImageMissingPlan(t *testing.T) {
	for _, analyzed := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh-statistics", true: "analyzed"}[analyzed], func(t *testing.T) {
			f, l, _ := baselineComparisonFixture(t, 10000, 9500)
			if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE job_inventory SET(autovacuum_enabled=false); ALTER TABLE library_inventory_baseline_data SET(autovacuum_enabled=false); ALTER TABLE job_ignore_decisions SET(autovacuum_enabled=false); UPDATE library_inventory_baseline SET kind='image'; UPDATE job_inventory SET kind='image' WHERE path>'f-000100.mkv'`); err != nil {
				t.Fatal(err)
			}
			if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_decisions(job_id,root_id,path,outcome,rule_directory,rule_line,matched_path,reason) SELECT $1::uuid,$2::uuid,'f-'||lpad(n::text,6,'0')||'.mkv','included_missing','',0,'','' FROM generate_series(9501,9950)n`, l.Job.ID, f.registration.RootID); err != nil {
				t.Fatal(err)
			}
			if analyzed {
				if _, err := f.s.Pool.Exec(f.ctx, `ANALYZE job_inventory; ANALYZE library_inventory_baseline_data; ANALYZE job_ignore_decisions`); err != nil {
					t.Fatal(err)
				}
			}
			var missing int
			query := ignoreImageMissingSQL(false)
			if err := f.s.Pool.QueryRow(f.ctx, query, f.registration.Library.ID, l.Job.ID).Scan(&missing); err != nil || missing != 550 {
				t.Fatal("image missing transitions/classifications", missing, err)
			}
			plan := nfoWorkerPlan(t, f, query, f.registration.Library.ID, l.Job.ID)
			visited := float64(0)
			walkNFOWorkerPlan(plan, func(p nfoWorkerPlanNode) {
				if p.Relation == "job_inventory" || p.Relation == "library_inventory_baseline_data" || p.Relation == "job_ignore_decisions" {
					visited += (p.Rows + p.Removed) * p.Loops
				}
			})
			if visited > 3*(10000+9500+450) {
				t.Fatalf("ignore image query repeatedly traversed inventory: %g", visited)
			}
			t.Logf("10,000 baseline / 9,500 current / 450 decisions: visited %g relation rows", visited)
		})
	}
}
