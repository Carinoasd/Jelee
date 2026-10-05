//go:build !race && (linux || windows)

package scan

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	ignoresource "github.com/MoYuanCN/Jelee/internal/adapter/media/ignore"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

// The owner decision E2 aliases are read through the same rule entry point by
// both job modes: the custom-only mode and the custom+legacy family mode.
func TestIgnoreRuleAliasesInBothScanModes(t *testing.T) {
	names := ignoresource.RuleFileNames()
	if len(names) != 3 {
		t.Fatal("unexpected rule file names")
	}
	native, first, second := names[0], names[1], names[2]
	root := filepath.Clean(t.TempDir())
	for name, text := range map[string]string{
		first: "custom.tmp\n!keep.mkv\n", ".ignore": "*.mkv\n",
		"custom.tmp": "c", "keep.mkv": "k", "drop.mkv": "d", "plain.txt": "p",
		// The native file wins over an alias in the same directory.
		"shadow/" + native: "native.tmp\n", "shadow/" + second: "alias.tmp\n", "shadow/native.tmp": "n", "shadow/alias.tmp": "a",
	} {
		writeScanFile(t, root, name, []byte(text))
	}
	check := func(t *testing.T, excluded map[string]string, kept map[string]bool) {
		t.Helper()
		if excluded["custom.tmp"] != "." || excluded["shadow/native.tmp"] != "shadow" {
			t.Fatal("alias or native custom rule lost", excluded)
		}
		if _, ok := excluded["shadow/alias.tmp"]; ok || !kept["shadow/alias.tmp"] || !kept["plain.txt"] {
			t.Fatal("alias in the native file's directory was applied", excluded)
		}
	}
	t.Run("jeleeignore", func(t *testing.T) {
		excluded, kept := map[string]string{}, map[string]bool{}
		intent := domain.IgnoreIntent{Mode: domain.IgnoreModeJeleeignore, CaseMode: domain.IgnoreCaseSensitive}
		for _, dir := range []string{".", "shadow"} {
			err := NewIgnoreScanner().ScanIgnoreDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: dir}, intent, func(b domain.IgnoreScanBatch) error {
				for _, e := range b.Excluded {
					excluded[e.Path] = e.RuleDirectory
				}
				for _, e := range b.Inventory.Entries {
					kept[e.Path] = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		check(t, excluded, kept)
		if _, ok := excluded["drop.mkv"]; ok || !kept["drop.mkv"] || !kept["keep.mkv"] {
			t.Fatal("custom-only mode applied the legacy file")
		}
	})
	t.Run("family", func(t *testing.T) {
		runner, err := process.NewIgnoreRunner(t.TempDir(), 2, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		s := NewFamilyIgnoreScanner(runner)
		excluded, kept := map[string]string{}, map[string]bool{}
		families := map[string]string{}
		intent := domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}
		for _, dir := range []string{".", "shadow"} {
			err = s.ScanFamilyIgnoreDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: dir}, intent, func(b domain.FamilyIgnoreScanBatch) error {
				for _, e := range b.Excluded {
					excluded[e.Path], families[e.Path] = e.RuleDirectory, e.Family
				}
				for _, e := range b.Inventory.Entries {
					kept[e.Path] = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		check(t, excluded, kept)
		if families["custom.tmp"] != domain.IgnoreFamilyCustom || families["drop.mkv"] != domain.IgnoreFamilyLegacy || !kept["keep.mkv"] {
			t.Fatal("family provenance with an alias source", families)
		}
		if runner.Stats().Active != 0 {
			t.Fatal("helper survived scan")
		}
	})
}
