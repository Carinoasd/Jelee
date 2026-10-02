package postgres

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func familyComparisonFixture(t *testing.T) (jobFixture, domain.JobLease) {
	t.Helper()
	f, l, d, b := familyScanFixture(t)
	b.Inventory.Done = true
	if err := f.s.SaveFamilyIgnoreScanBatch(f.ctx, l, d, b); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO library_inventory_baseline(library_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision)
 SELECT $1::uuid,$2::uuid,'f-'||lpad(n::text,6,'0')||'.mkv',true,'video',7,1,inventory_generation,inventory_baseline_revision FROM libraries CROSS JOIN generate_series(1,130)n WHERE id=$1::uuid`, f.registration.Library.ID, d.RootID)
	if err != nil {
		t.Fatal(err)
	}
	return f, l
}

func TestFamilyBaselineComparisonPageAndIsolation(t *testing.T) {
	f, l := familyComparisonFixture(t)
	if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("old mode admitted family", err)
	}
	for range 2 {
		if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	p, err := f.s.NextFamilyIgnoreBaselinePage(f.ctx, l)
	if err != nil || p.RawCount != 128 || len(p.Unseen) != 128 || p.End || p.Complete || p.Token.Sequence != 0 || p.Unseen[0].Path != "f-000001.mkv" || p.Unseen[127].Path != "f-000128.mkv" {
		t.Fatal("raw page", err)
	}
	if _, err = f.s.NextIgnoreBaselinePage(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("old reader admitted family", err)
	}
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, l, p.Token, decisionPage(p, domain.IgnoreBaselineMissing)); err != domain.ErrConflict {
		t.Fatal("old writer admitted family", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE job_inventory SET size=size+1 WHERE job_id=$1::uuid`, l.Job.ID); err == nil {
		t.Fatal("comparison did not freeze inventory")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `DELETE FROM job_ignore_family_exclusions WHERE job_id=$1::uuid`, l.Job.ID); err == nil {
		t.Fatal("comparison did not freeze family exclusions")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.NextFamilyIgnoreBaselinePage(f.ctx, l); err != domain.ErrInventoryInvalidated {
		t.Fatal("changed baseline revision accepted", err)
	}
}

func TestFamilyBaselineComparisonMigration(t *testing.T) {
	f, l := familyComparisonFixture(t)
	if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.NextFamilyIgnoreBaselinePage(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	query := `INSERT INTO job_ignore_family_decisions(job_id,root_id,path,outcome,family,reason,rule_directory,rule_line,matched_path) VALUES($1::uuid,$2::uuid,'hidden/movie.mkv','excluded',$3,$4,'hidden',$5,'hidden')`
	for _, bad := range []struct {
		family, reason string
		line           int
	}{{domain.IgnoreFamilyCustom, domain.IgnoreReasonRule, 1}, {domain.IgnoreFamilyLegacy, domain.IgnoreReasonBlank, 1}, {domain.IgnoreFamilyLegacy, domain.IgnoreReasonRule, 0}} {
		if _, err = f.s.Pool.Exec(f.ctx, query, l.Job.ID, p.Unseen[0].RootID, bad.family, bad.reason, bad.line); err == nil {
			t.Fatal("invalid decision shape accepted")
		}
	}
	if _, err = f.s.Pool.Exec(f.ctx, query, l.Job.ID, p.Unseen[0].RootID, domain.IgnoreFamilyLegacy, domain.IgnoreReasonBlank, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_family_decisions SET reason='invalid-source' WHERE job_id=$1::uuid`, l.Job.ID); err == nil {
		t.Fatal("decision mutated")
	}
	nfoMigrationDenied(t, f, "000018_ignore_family_baseline.down.sql")
	if _, err = f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal("history cascade", err)
	}
	nfoMigrateVersion(t, f, "down", 24)
	nfoMigrateVersion(t, f, "down", 23)
	nfoMigrateVersion(t, f, "down", 22)
	nfoMigrateVersion(t, f, "down", 21)
	nfoMigrateVersion(t, f, "down", 20)
	nfoMigrateVersion(t, f, "down", 19)
	nfoMigrateVersion(t, f, "down", 18)
	nfoMigrateVersion(t, f, "down", 17)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
}

func TestFamilyBaselineComparisonRejectsInvalidSources(t *testing.T) {
	for _, table := range []string{"job_ignore_manifests", "job_ignore_legacy_manifests"} {
		t.Run(table, func(t *testing.T) {
			f, l := familyComparisonFixture(t)
			if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE `+table+` SET invalidated=true WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.NextFamilyIgnoreBaselinePage(f.ctx, l); err != domain.ErrInventoryInvalidated {
				t.Fatal("invalid source accepted", err)
			}
		})
	}
}

func TestFamilyBaselineComparisonRequiresDrainedScan(t *testing.T) {
	f, l, d, b := familyScanFixture(t)
	if err := f.s.SaveFamilyIgnoreScanBatch(f.ctx, l, d, b); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("incomplete scan accepted", err)
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_comparisons WHERE job_id=$1::uuid`, l.Job.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial comparison persisted", err)
	}
}
