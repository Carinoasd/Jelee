package postgres

import (
	"context"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func familyScanFixture(t *testing.T) (jobFixture, domain.JobLease, domain.ScanDirectory, domain.FamilyIgnoreScanBatch) {
	t.Helper()
	f, l, legacy := legacyManifestFixture(t)
	custom := legacy.Proofs[0].IgnoreDirectoryProof
	legacy.Proofs[0].RuleSize = 0
	d, err := f.s.NextFamilyIgnoreScanDirectory(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	child := manifestChild(custom, "hidden")
	hidden := domain.LegacyIgnoreObservation{Version: legacy.Version, Directory: "hidden", Proofs: []domain.LegacyIgnoreDirectoryProof{legacy.Proofs[0], {IgnoreDirectoryProof: child, Checked: true}}}
	b := domain.FamilyIgnoreScanBatch{CustomProofs: []domain.IgnoreDirectoryProof{custom}, HeldDirectoryIdentity: custom.Identity, LegacyObservations: []domain.LegacyIgnoreObservation{legacy, hidden}, Inventory: domain.ScanBatch{Entries: []domain.InventoryEntry{{RootID: d.RootID, Path: "kept.mkv", Kind: "video", Size: 7, ModifiedUnixNano: 1}}}, Excluded: []domain.FamilyIgnoreExclusion{
		{Path: "custom.mkv", Kind: "video", Family: domain.IgnoreFamilyCustom, Reason: domain.IgnoreReasonRule, RuleDirectory: ".", RuleLine: 1, MatchedPath: "custom.mkv"},
		{Path: "legacy.mkv", Kind: "video", Family: domain.IgnoreFamilyLegacy, Reason: domain.IgnoreReasonBlank, RuleDirectory: ".", MatchedPath: "legacy.mkv"},
		{Path: "hidden", Kind: "directory", Family: domain.IgnoreFamilyLegacy, Reason: domain.IgnoreReasonBlank, RuleDirectory: ".", MatchedPath: "hidden"},
	}}
	return f, l, d, b
}

func TestFamilyIgnoreScanReplayRestartAndMigration(t *testing.T) {
	f, l, d, b := familyScanFixture(t)
	for i := 0; i < 2; i++ {
		if err := f.s.SaveFamilyIgnoreScanBatch(f.ctx, l, d, b); err != nil {
			t.Fatal(err)
		}
	}
	if files, dirs := scanExclusionCounts(t, f, l.Job.ID); files != 2 || dirs != 1 {
		t.Fatal("replay counts", files, dirs)
	}
	var blank int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_family_exclusions WHERE job_id=$1::uuid AND reason='blank-source' AND rule_line=0`, l.Job.ID).Scan(&blank); err != nil || blank != 2 {
		t.Fatal("zero-line provenance", err, blank)
	}
	next, err := f.s.NextFamilyIgnoreScanDirectory(f.ctx, l)
	if err != nil || next != d {
		t.Fatal("restart", err)
	}
	if files, dirs := scanExclusionCounts(t, f, l.Job.ID); files != 0 || dirs != 0 {
		t.Fatal("restart counts")
	}
	if f.get(t, l.Job.ID).Files != 0 {
		t.Fatal("restart inventory")
	}
	b.Inventory.Done = true
	if err = f.s.SaveFamilyIgnoreScanBatch(f.ctx, l, d, b); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.NextFamilyIgnoreScanDirectory(f.ctx, l); err != domain.ErrNotFound {
		t.Fatal("excluded directory queued", err)
	}
	nfoMigrationDenied(t, f, "000015_ignore_family_scan.down.sql")
	if _, err = f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal("cascade", err)
	}
	nfoMigrateVersion(t, f, "down", 39)
	nfoMigrateVersion(t, f, "down", 38)
	nfoMigrateVersion(t, f, "down", 37)
	nfoMigrateVersion(t, f, "down", 36)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "down", 31)
	nfoMigrateVersion(t, f, "down", 30)
	nfoMigrateVersion(t, f, "down", 29)
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
}

func TestFamilyIgnoreScanSourceConflictRollsBackBothFamilies(t *testing.T) {
	f, l, d, b := familyScanFixture(t)
	b.Inventory = domain.ScanBatch{Directories: []string{"child"}, Done: true}
	b.Excluded = nil
	b.LegacyObservations = b.LegacyObservations[:1]
	if err := f.s.SaveFamilyIgnoreScanBatch(f.ctx, l, d, b); err != nil {
		t.Fatal(err)
	}
	child, err := f.s.NextFamilyIgnoreScanDirectory(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	p := manifestChild(b.CustomProofs[0], "child")
	b.CustomProofs = append(b.CustomProofs, p)
	b.HeldDirectoryIdentity = p.Identity
	legacy := b.LegacyObservations[0]
	legacy.Directory = "child"
	legacy.Proofs = append(append([]domain.LegacyIgnoreDirectoryProof(nil), legacy.Proofs...), domain.LegacyIgnoreDirectoryProof{IgnoreDirectoryProof: p, Checked: true})
	legacy.Proofs[0].RuleSHA256 = [32]byte{99}
	b.LegacyObservations = []domain.LegacyIgnoreObservation{legacy}
	b.Inventory = domain.ScanBatch{Entries: []domain.InventoryEntry{{RootID: d.RootID, Path: "child/file.mkv", Kind: "video", Size: 1}}}
	if err = f.s.SaveFamilyIgnoreScanBatch(f.ctx, l, child, b); err != domain.ErrInventoryInvalidated {
		t.Fatal("source conflict", err)
	}
	n, _, _, invalid := manifestCounts(t, f, l.Job.ID)
	ln, lq, _, _, linvalid := legacyCounts(t, f, l)
	if n != 1 || ln != 1 || lq != 1 || !invalid || !linvalid {
		t.Fatal("partial evidence survived", n, ln, lq, invalid, linvalid)
	}
	if f.get(t, l.Job.ID).Files != 0 {
		t.Fatal("partial inventory survived")
	}
}

func TestFamilyIgnoreScanRejectsMalformedProvenance(t *testing.T) {
	for _, mode := range []string{"identity", "query", "family", "reason", "line", "source", "overlap"} {
		t.Run(mode, func(t *testing.T) {
			f, l, d, b := familyScanFixture(t)
			switch mode {
			case "identity":
				b.LegacyObservations[0].Proofs[0].Identity[0]++
			case "query":
				b.LegacyObservations = b.LegacyObservations[:1]
			case "family":
				b.Excluded[1].Family = "other"
			case "reason":
				b.Excluded[1].Reason = "other"
			case "line":
				b.Excluded[1].RuleLine = 1
			case "source":
				b.Excluded[1].RuleDirectory = "hidden"
			case "overlap":
				b.Excluded[1].Path = "kept.mkv"
				b.Excluded[1].MatchedPath = "kept.mkv"
			}
			if err := f.s.SaveFamilyIgnoreScanBatch(f.ctx, l, d, b); err != domain.ErrInvalid {
				t.Fatal("malformed accepted", err)
			}
			if f.get(t, l.Job.ID).Files != 0 {
				t.Fatal("malformed batch persisted")
			}
		})
	}
}

func TestFamilyIgnoreScanCancelAndFrozen(t *testing.T) {
	for _, mode := range []string{"cancel", "frozen", "budget"} {
		t.Run(mode, func(t *testing.T) {
			f, l, d, b := familyScanFixture(t)
			want := domain.ErrConflict
			switch mode {
			case "cancel":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				want = context.Canceled
			case "frozen":
				if err := f.s.RecordLegacyIgnoreObservations(f.ctx, l, b.LegacyObservations); err != nil {
					t.Fatal(err)
				}
				if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
					t.Fatal(err)
				}
			case "budget":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET max_directories=2 WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				b.Inventory.Directories = []string{"extra"}
				want = domain.ErrScanLimit
			}
			if err := f.s.SaveFamilyIgnoreScanBatch(f.ctx, l, d, b); err != want {
				t.Fatal("guard", err, want)
			}
			if f.get(t, l.Job.ID).Files != 0 {
				t.Fatal("rejected batch saved inventory")
			}
		})
	}
}

func TestFamilyIgnoreScanDatabaseFreezeAndModeGuards(t *testing.T) {
	f, l, d, b := familyScanFixture(t)
	if err := f.s.SaveFamilyIgnoreScanBatch(f.ctx, l, d, b); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM job_ignore_family_exclusions WHERE job_id=$1::uuid`, l.Job.ID); err == nil {
		t.Fatal("frozen exclusions removed")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM job_inventory WHERE job_id=$1::uuid`, l.Job.ID); err == nil {
		t.Fatal("frozen inventory removed")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal("frozen cascade", err)
	}
	old, lease, dir, original := ignoreScanFixture(t)
	b.CustomProofs = original.Proofs
	b.HeldDirectoryIdentity = original.HeldDirectoryIdentity
	b.Inventory = original.Inventory
	b.Excluded = nil
	b.LegacyObservations = []domain.LegacyIgnoreObservation{{Version: domain.LegacyIgnoreProofVersion, Directory: dir.Path, Proofs: []domain.LegacyIgnoreDirectoryProof{{IgnoreDirectoryProof: original.Proofs[0], Checked: true}}}}
	if err := old.s.SaveFamilyIgnoreScanBatch(old.ctx, lease, dir, b); err != domain.ErrConflict {
		t.Fatal("old mode accepted family batch", err)
	}
}
