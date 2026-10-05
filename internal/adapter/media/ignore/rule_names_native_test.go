//go:build linux || windows

package ignoresource

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

// aliasNames avoids spelling the upstream names outside rule_names.go.
func aliasNames(t *testing.T) (native, first, second string) {
	t.Helper()
	names := RuleFileNames()
	if len(names) != 3 || names[0] != ".jeleeignore" {
		t.Fatal("unexpected rule file names")
	}
	names[1] = "poison"
	if RuleFileNames()[1] == "poison" {
		t.Fatal("rule file names share caller memory")
	}
	return ruleFileNames[0], ruleFileNames[1], ruleFileNames[2]
}

func evaluateNative(t *testing.T, root, candidate string) ignore.Match {
	t.Helper()
	o, err := NewResolver().Evaluate(context.Background(), root, candidate, ignore.File, ignore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return o.Match
}

func TestRuleAliasesApplyWithNativeSemantics(t *testing.T) {
	_, first, second := aliasNames(t)
	for _, alias := range []string{first, second} {
		t.Run(alias[1:], func(t *testing.T) {
			root := filepath.Clean(t.TempDir())
			writeRule(t, root, alias, "*.tmp\n/anchored.mkv\n")
			writeRule(t, root, "child/"+alias, "!keep.tmp\n")
			if m := evaluateNative(t, root, "a.tmp"); m.Outcome != ignore.Exclude || m.Source != ".jeleeignore" || m.Line != 1 {
				t.Fatal("alias rule not applied as the directory source", m)
			}
			if m := evaluateNative(t, root, "anchored.mkv"); m.Outcome != ignore.Exclude {
				t.Fatal("anchored alias rule lost")
			}
			if m := evaluateNative(t, root, "child/anchored.mkv"); m.Outcome == ignore.Exclude {
				t.Fatal("alias anchor escaped its directory")
			}
			if m := evaluateNative(t, root, "child/keep.tmp"); m.Outcome != ignore.Include || m.Source != "child/.jeleeignore" {
				t.Fatal("nested alias negation lost", m)
			}
		})
	}
}

func TestRuleAliasPrecedenceWithinOneDirectory(t *testing.T) {
	native, first, second := aliasNames(t)
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, native, "native.tmp\n")
	writeRule(t, root, first, "first.tmp\n")
	writeRule(t, root, second, "second.tmp\n")
	if evaluateNative(t, root, "native.tmp").Outcome != ignore.Exclude || evaluateNative(t, root, "first.tmp").Outcome == ignore.Exclude || evaluateNative(t, root, "second.tmp").Outcome == ignore.Exclude {
		t.Fatal("native file must shadow both aliases in the same directory")
	}
	if err := os.Remove(filepath.Join(root, native)); err != nil {
		t.Fatal(err)
	}
	if evaluateNative(t, root, "first.tmp").Outcome != ignore.Exclude || evaluateNative(t, root, "second.tmp").Outcome == ignore.Exclude {
		t.Fatal("first alias must shadow the second")
	}
	if err := os.Remove(filepath.Join(root, first)); err != nil {
		t.Fatal(err)
	}
	if evaluateNative(t, root, "second.tmp").Outcome != ignore.Exclude {
		t.Fatal("second alias not used alone")
	}
	// An alias in a child directory still inherits the parent's rules.
	writeRule(t, root, native, "*.log\n")
	writeRule(t, root, "child/"+second, "!keep.log\n")
	if evaluateNative(t, root, "child/a.log").Outcome != ignore.Exclude || evaluateNative(t, root, "child/keep.log").Outcome != ignore.Include {
		t.Fatal("alias broke directory inheritance")
	}
}

func TestRuleAliasUnsafeNativeEntryDoesNotFallBack(t *testing.T) {
	native, first, _ := aliasNames(t)
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, first, "*.tmp\n")
	if err := os.Mkdir(filepath.Join(root, native), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewResolver().Evaluate(context.Background(), root, "a.tmp", ignore.File, ignore.Options{}); err != ErrUnsafe {
		t.Fatal("unsafe native entry silently replaced by alias", err)
	}
	if err := os.Remove(filepath.Join(root, native)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir", first), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewResolver().Evaluate(context.Background(), root, "dir/a.tmp", ignore.File, ignore.Options{}); err != ErrUnsafe {
		t.Fatal("unsafe alias entry accepted", err)
	}
}

func TestRuleAliasChangesInvalidateRetainedProofs(t *testing.T) {
	native, first, _ := aliasNames(t)
	for _, change := range []string{"native-appears", "alias-removed", "alias-edited"} {
		t.Run(change, func(t *testing.T) {
			root := filepath.Clean(t.TempDir())
			writeRule(t, root, first, "*.tmp\n")
			writeRule(t, root, "child/movie.mkv", "media")
			r := NewResolver()
			before, err := r.ReobserveDirectory(context.Background(), root, "child")
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != 2 || !before[0].RulePresent {
				t.Fatal("alias source not recorded in the directory proof")
			}
			switch change {
			case "native-appears":
				writeRule(t, root, native, "*.tmp\n")
			case "alias-removed":
				if err := os.Remove(filepath.Join(root, first)); err != nil {
					t.Fatal(err)
				}
			case "alias-edited":
				writeRule(t, root, first, "*.log\n")
			}
			after, err := r.ReobserveDirectory(context.Background(), root, "child")
			if err != nil {
				t.Fatal(err)
			}
			if after[0] == before[0] {
				t.Fatal("alias change kept the same source proof")
			}
		})
	}
}

func TestRuleAliasScanAndVerification(t *testing.T) {
	_, first, second := aliasNames(t)
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, first, "*.tmp\n")
	writeRule(t, root, "child/"+second, "!keep.tmp\n")
	writeRule(t, root, "child/drop.tmp", "x")
	writeRule(t, root, "child/keep.tmp", "x")
	excluded, kept, done := map[string]bool{}, map[string]bool{}, 0
	err := NewResolver().ScanDirectory(context.Background(), root, "child", ignore.Options{}, func(b ScanBatch) error {
		for _, e := range b.Entries {
			if e.Match.Outcome == ignore.Exclude {
				excluded[e.Path] = true
			} else {
				kept[e.Path] = true
			}
		}
		if b.Done {
			done++
		}
		return nil
	})
	if err != nil || done != 1 || !excluded["child/drop.tmp"] || !kept["child/keep.tmp"] {
		t.Fatal("alias scan", err, excluded, kept)
	}
}

func TestRuleAliasesAreNotLegacyNearestSources(t *testing.T) {
	_, first, second := aliasNames(t)
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".ignore", "legacy\n")
	writeRule(t, root, "child/"+first, "custom\n")
	writeRule(t, root, "child/"+second, "custom\n")
	o, err := NewResolver().ObserveLegacy(context.Background(), root, "child")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Present || o.SourceDirectory != "." || string(o.Bytes()) != "legacy\n" {
		t.Fatal("alias files were treated as legacy nearest sources")
	}
	// Conversely, the legacy file never becomes a custom alias.
	if m := evaluateNative(t, root, "legacy"); m.Outcome == ignore.Exclude {
		t.Fatal("legacy source treated as a custom alias")
	}
}
