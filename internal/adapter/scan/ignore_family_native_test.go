//go:build !race && (linux || windows)

package scan

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

func TestFamilyIgnoreNativeHelper(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	for name, text := range map[string]string{
		".jeleeignore": "!keep.mkv\ncustom.tmp\n!allowed/\n",
		".ignore":      "*.mkv\n", "keep.mkv": "keep", "drop.mkv": "drop", "custom.tmp": "custom", "plain.txt": "plain",
		"blocked/.ignore": "", "blocked/file.mkv": "unvisited", "allowed/.ignore": "", "invalid/.ignore": "[",
	} {
		writeScanFile(t, root, name, []byte(text))
	}
	runner, err := process.NewIgnoreRunner(t.TempDir(), 2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	s := NewFamilyIgnoreScanner(runner)
	kept := map[string]bool{}
	excluded := map[string]domain.FamilyIgnoreExclusion{}
	done := 0
	err = s.ScanFamilyIgnoreDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}, func(b domain.FamilyIgnoreScanBatch) error {
		if b.HeldDirectoryIdentity == ([32]byte{}) || len(b.CustomProofs) == 0 || len(b.LegacyObservations) == 0 {
			t.Fatal("missing source binding")
		}
		for _, o := range b.LegacyObservations {
			if domain.ValidateLegacyIgnoreObservation(o) != nil {
				t.Fatal("invalid family observation")
			}
		}
		for _, e := range b.Inventory.Entries {
			kept[e.Path] = true
		}
		for _, p := range b.Inventory.Directories {
			kept[p] = true
		}
		for _, e := range b.Excluded {
			excluded[e.Path] = e
		}
		if b.Inventory.Done {
			done++
		}
		return nil
	})
	if err != nil || done != 1 {
		t.Fatal("family scan", err)
	}
	for _, name := range []string{"keep.mkv", "plain.txt", "allowed"} {
		if !kept[name] {
			t.Fatal("explicit custom include or unmatched file lost", name)
		}
	}
	if excluded["drop.mkv"].Family != domain.IgnoreFamilyLegacy || excluded["drop.mkv"].RuleLine != 1 || excluded["custom.tmp"].Family != domain.IgnoreFamilyCustom {
		t.Fatal("family provenance lost")
	}
	if excluded["blocked"].Reason != domain.IgnoreReasonBlank || excluded["blocked"].RuleLine != 0 || excluded["blocked"].RuleDirectory != "blocked" {
		t.Fatal("empty directory source lost")
	}
	if excluded["invalid"].Reason != domain.IgnoreReasonInvalid || excluded["invalid"].RuleLine != 0 {
		t.Fatal("invalid-source policy lost")
	}
	if runner.Stats().Active != 0 {
		t.Fatal("helper survived scan")
	}
}
