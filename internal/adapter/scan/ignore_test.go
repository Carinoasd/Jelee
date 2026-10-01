//go:build linux || windows

package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestIgnoreBridgeNativeBatchProvenance(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".jeleeignore", []byte("*.tmp\n"))
	writeScanFile(t, root, "child/.jeleeignore", []byte("!keep.tmp\nhidden/\n"))
	writeScanFile(t, root, "child/skip.tmp", []byte("excluded"))
	writeScanFile(t, root, "child/keep.tmp", []byte("kept"))
	writeScanFile(t, root, "child/movie.MKV", []byte("video"))
	writeScanFile(t, root, "child/hidden/file.mkv", []byte("hidden"))
	d := domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "child"}
	intent := domain.IgnoreIntent{Mode: domain.IgnoreModeJeleeignore, CaseMode: domain.IgnoreCaseSensitive}
	kept := map[string]domain.InventoryEntry{}
	excluded := map[string]domain.IgnoreScanExclusion{}
	done := 0
	err := NewIgnoreScanner().ScanIgnoreDirectory(context.Background(), d, intent, func(b domain.IgnoreScanBatch) error {
		if len(b.Proofs) != 2 || b.HeldDirectoryIdentity != b.Proofs[1].Identity {
			t.Fatal("lost directory binding")
		}
		for _, p := range b.Proofs {
			if domain.ValidateIgnoreDirectoryProof(p) != nil {
				t.Fatal("invalid translated proof")
			}
		}
		if len(b.Inventory.Directories) != 0 {
			t.Fatal("excluded directory queued")
		}
		for _, e := range b.Inventory.Entries {
			kept[e.Path] = e
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
		t.Fatal("scan failed", err, done)
	}
	if kept["child/movie.MKV"].Kind != "video" || kept["child/keep.tmp"].Size != 4 {
		t.Fatal("metadata lost")
	}
	if e := excluded["child/skip.tmp"]; e.RuleDirectory != "." || e.RuleLine != 1 || e.MatchedPath != e.Path {
		t.Fatal("ancestor provenance lost", e)
	}
	if e := excluded["child/hidden"]; e.RuleDirectory != "child" || e.RuleLine != 2 || e.Kind != "directory" {
		t.Fatal("local provenance lost", e)
	}
	sentinel := errors.New("repository failure")
	if err = NewIgnoreScanner().ScanIgnoreDirectory(context.Background(), d, intent, func(domain.IgnoreScanBatch) error { return sentinel }); err != sentinel {
		t.Fatal("callback error changed", err)
	}
}

func TestIgnoreBridgeBaselineAndReobserve(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".jeleeignore", []byte("hidden/\n*.TMP\n"))
	writeScanFile(t, root, "nested/child/file.mkv", []byte("video"))
	scanner := NewIgnoreScanner()
	intent := domain.IgnoreIntent{Mode: domain.IgnoreModeJeleeignore, CaseMode: domain.IgnoreCaseSensitive}
	for _, tc := range []struct{ path, outcome, matched string }{
		{"hidden/deep/file.mkv", domain.IgnoreBaselineExcluded, "hidden"},
		{"absent/deep/file.mkv", domain.IgnoreBaselineMissing, ""},
		{"lost.tmp", domain.IgnoreBaselineMissing, ""},
	} {
		d, p, err := scanner.EvaluateIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: tc.path}, intent)
		if err != nil || d.Outcome != tc.outcome || d.MatchedPath != tc.matched || len(p) == 0 {
			t.Fatal("wrong baseline observation", tc.path, err)
		}
		if tc.path == "absent/deep/file.mkv" && (!p[len(p)-1].MissingDirectory || p[len(p)-1].Directory != "absent") {
			t.Fatal("missing ancestor proof lost")
		}
	}
	intent.CaseMode = domain.IgnoreCaseASCIIInsensitive
	d, _, err := scanner.EvaluateIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "lost.tmp"}, intent)
	if err != nil || d.Outcome != domain.IgnoreBaselineExcluded {
		t.Fatal("case intent ignored", err)
	}
	_, p, err := scanner.EvaluateIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "nested/child/lost.mkv"}, intent)
	if err != nil {
		t.Fatal(err)
	}
	retained := p[len(p)-1]
	observed, err := scanner.ReobserveIgnoreProof(context.Background(), root, retained)
	if err != nil || observed != retained {
		t.Fatal("unchanged proof differs", err)
	}
	writeScanFile(t, root, "nested/child/.jeleeignore", []byte("*.mkv\n"))
	observed, err = scanner.ReobserveIgnoreProof(context.Background(), root, retained)
	if err != nil || observed == retained || !observed.RulePresent {
		t.Fatal("new source hidden", err)
	}
	// Move only the owned test directory; no user media is touched.
	if err = os.Rename(filepath.Join(root, "nested"), filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	observed, err = scanner.ReobserveIgnoreProof(context.Background(), root, retained)
	if err != domain.ErrInventoryInvalidated || observed != (domain.IgnoreDirectoryProof{}) {
		t.Fatal("earlier absence fabricated descendant proof", err)
	}
	d, p, err = scanner.EvaluateIgnoreBaseline(context.Background(), filepath.Join(root, "missing-root"), domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "lost.mkv"}, intent)
	if err == nil || d != (domain.IgnoreBaselineDecision{}) || p != nil {
		t.Fatal("unavailable root classified missing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, p, err = scanner.EvaluateIgnoreBaseline(ctx, root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "lost.mkv"}, intent)
	if err != context.Canceled || d != (domain.IgnoreBaselineDecision{}) || p != nil {
		t.Fatal("cancellation returned evidence", err)
	}
}
