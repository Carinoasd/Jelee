package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func ignoreScanFixture(t *testing.T) (jobFixture, domain.JobLease, domain.ScanDirectory, domain.IgnoreScanBatch) {
	t.Helper()
	f, l, root := manifestFixture(t)
	d, err := f.s.NextIgnoreScanDirectory(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	b := domain.IgnoreScanBatch{Proofs: []domain.IgnoreDirectoryProof{root}, HeldDirectoryIdentity: root.Identity, Inventory: domain.ScanBatch{Entries: []domain.InventoryEntry{{RootID: d.RootID, Path: "kept.mkv", Kind: "video", Size: 7, ModifiedUnixNano: 1}}}, Excluded: []domain.IgnoreScanExclusion{{Path: "excluded.mkv", Kind: "video", RuleDirectory: ".", RuleLine: 1, MatchedPath: "excluded.mkv"}, {Path: "hidden", Kind: "directory", RuleDirectory: ".", RuleLine: 1, MatchedPath: "hidden"}}}
	return f, l, d, b
}

func scanExclusionCounts(t *testing.T, f jobFixture, id string) (int64, int64) {
	t.Helper()
	var files, dirs int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT excluded_files,excluded_directories FROM job_ignore_scan_state WHERE job_id=$1::uuid`, id).Scan(&files, &dirs); err != nil {
		t.Fatal(err)
	}
	return files, dirs
}

func TestIgnoreScanAtomicReplayRestartAndPublication(t *testing.T) {
	f, l, d, b := ignoreScanFixture(t)
	for i := 0; i < 2; i++ {
		if err := f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); err != nil {
			t.Fatal(err)
		}
	}
	if files, dirs := scanExclusionCounts(t, f, l.Job.ID); files != 1 || dirs != 1 {
		t.Fatal("replay duplicated exclusions")
	}
	if j := f.get(t, l.Job.ID); j.Files != 1 || j.Bytes != 7 {
		t.Fatal("replay duplicated inventory", j)
	}
	d2, err := f.s.NextIgnoreScanDirectory(f.ctx, l)
	if err != nil || d2 != d {
		t.Fatal("restart selected another directory", err)
	}
	if files, dirs := scanExclusionCounts(t, f, l.Job.ID); files != 0 || dirs != 0 {
		t.Fatal("restart retained partial exclusion counts")
	}
	if j := f.get(t, l.Job.ID); j.Files != 0 || j.Bytes != 0 {
		t.Fatal("restart retained partial inventory")
	}
	count, _, _, _ := manifestCounts(t, f, l.Job.ID)
	if count != 1 {
		t.Fatal("restart repinned sources")
	}
	b.Inventory.Done = true
	if err = f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.NextIgnoreScanDirectory(f.ctx, l); err != domain.ErrNotFound {
		t.Fatal("excluded directory entered frontier", err)
	}
	classifyForPublication(t, f, l, func(*domain.IgnoreBaselineDecision) {}, true)
	if err = f.s.FinishIgnoreJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatal("exclusions published as observed inventory", err)
	}
	nfoMigrationDenied(t, f, "000012_ignore_scan.down.sql")
	if _, err = f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal("history cleanup blocked", err)
	}
	nfoMigrateVersion(t, f, "down", 28)
	nfoMigrateVersion(t, f, "down", 27)
	nfoMigrateVersion(t, f, "down", 26)
	nfoMigrateVersion(t, f, "down", 25)
	nfoMigrateVersion(t, f, "down", 24)
	nfoMigrateVersion(t, f, "down", 23)
	nfoMigrateVersion(t, f, "down", 22)
	nfoMigrateVersion(t, f, "down", 21)
	nfoMigrateVersion(t, f, "down", 20)
	nfoMigrateVersion(t, f, "down", 19)
	nfoMigrateVersion(t, f, "down", 18)
	nfoMigrateVersion(t, f, "down", 17)
	nfoMigrateVersion(t, f, "down", 16)
	nfoMigrateVersion(t, f, "down", 15)
	nfoMigrateVersion(t, f, "down", 14)
	nfoMigrateVersion(t, f, "down", 13)
	nfoMigrateVersion(t, f, "down", 12)
	nfoMigrateVersion(t, f, "down", 11)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
}

func TestIgnoreScanProvenanceAndOverlapRejected(t *testing.T) {
	for _, mode := range []string{"identity", "missing-proof", "rule", "overlap", "changed-replay"} {
		t.Run(mode, func(t *testing.T) {
			f, l, d, b := ignoreScanFixture(t)
			if mode == "changed-replay" {
				if err := f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); err != nil {
					t.Fatal(err)
				}
			}
			before := ignoreSnapshot(t, f)
			switch mode {
			case "identity":
				b.HeldDirectoryIdentity[0]++
			case "missing-proof":
				b.Proofs = nil
			case "rule":
				b.Excluded[0].RuleDirectory = "not-retained"
			case "overlap":
				b.Excluded[0].Path = "kept.mkv"
				b.Excluded[0].MatchedPath = "kept.mkv"
			case "changed-replay":
				b.Excluded[0].RuleLine++
			}
			if err := f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); err == nil || ignoreSnapshot(t, f) != before {
				t.Fatal("invalid batch accepted or mutated state", mode, err)
			}
		})
	}
}

func TestIgnoreScanChangedSourceInvalidatesWithoutInventoryChange(t *testing.T) {
	f, l, d, b := ignoreScanFixture(t)
	if err := f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); err != nil {
		t.Fatal(err)
	}
	before := ignoreSnapshot(t, f)
	b.Proofs[0].RuleSHA256[0]++
	b.Inventory.Entries[0].Size = 99
	if err := f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); err != domain.ErrInventoryInvalidated {
		t.Fatal("source change accepted", err)
	}
	if ignoreSnapshot(t, f) != before {
		t.Fatal("source change updated inventory")
	}
	_, _, _, invalid := manifestCounts(t, f, l.Job.ID)
	if !invalid {
		t.Fatal("source mismatch not durable")
	}
}

func TestIgnoreScanBudgetAndLateLeaseRollback(t *testing.T) {
	for _, mode := range []string{"files", "directories", "late"} {
		t.Run(mode, func(t *testing.T) {
			f, l, d, b := ignoreScanFixture(t)
			var err error
			if mode == "files" {
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET max_entries=100 WHERE id=$1::uuid`, l.Job.ID)
				b.Excluded = nil
				for i := 0; i < 100; i++ {
					name := fmt.Sprintf("excluded-%03d", i)
					b.Excluded = append(b.Excluded, domain.IgnoreScanExclusion{Path: name, Kind: "video", RuleDirectory: ".", RuleLine: 1, MatchedPath: name})
				}
			}
			if mode == "directories" {
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET max_directories=1 WHERE id=$1::uuid`, l.Job.ID)
			}
			if mode == "late" {
				_, err = f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE scan_entered; CREATE FUNCTION hold_scan() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('scan_entered'); PERFORM pg_sleep(0.2); RETURN NEW; END $$; CREATE TRIGGER hold_scan BEFORE INSERT ON job_ignore_scan_state FOR EACH ROW EXECUTE FUNCTION hold_scan()`)
				if err == nil {
					_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '120 milliseconds' WHERE id=$1::uuid`, l.Job.ID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := ignoreSnapshot(t, f)
			want := domain.ErrScanLimit
			if mode == "late" {
				want = domain.ErrJobLeaseLost
			}
			if err = f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); !errors.Is(err, want) || ignoreSnapshot(t, f) != before {
				t.Fatal("failed batch left partial observations", mode, err)
			}
			var count int
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM job_ignore_scan_state)+(SELECT count(*) FROM job_ignore_manifests)`).Scan(&count); err != nil || count != 0 {
				t.Fatal("atomic failure retained proofs or counters", err)
			}
			if mode == "late" {
				var entered bool
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM scan_entered`).Scan(&entered); err != nil || !entered {
					t.Fatal("delayed insert not reached", err)
				}
			}
		})
	}
}

func TestIgnoreScanComparisonFreezesExclusions(t *testing.T) {
	f, l, d, b := ignoreScanFixture(t)
	b.Inventory.Done = true
	if err := f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); err != domain.ErrConflict {
		t.Fatal("frozen save accepted", err)
	}
	if _, err := f.s.NextIgnoreScanDirectory(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("frozen restart accepted", err)
	}
	for _, query := range []string{
		`DELETE FROM job_ignore_exclusions WHERE job_id=$1::uuid`,
		`UPDATE job_ignore_exclusions SET rule_line=2 WHERE job_id=$1::uuid`,
		`DELETE FROM job_directories WHERE job_id=$1::uuid`,
	} {
		if _, err := f.s.Pool.Exec(f.ctx, query, l.Job.ID); err == nil {
			t.Fatal("frozen report mutation accepted")
		}
	}
	if files, dirs := scanExclusionCounts(t, f, l.Job.ID); files != 1 || dirs != 1 {
		t.Fatal("frozen counts changed")
	}
	var rows int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_exclusions WHERE job_id=$1::uuid`, l.Job.ID).Scan(&rows); err != nil || rows != 2 {
		t.Fatal("frozen exclusions changed", err)
	}
}
