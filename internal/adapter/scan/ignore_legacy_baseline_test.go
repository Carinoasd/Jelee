//go:build linux || windows

package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
)

func TestLegacyBaselineMatchRejectsReappearingParent(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("*.mkv"))
	evaluator := legacyEvaluatorFunc(func(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		if err := os.Mkdir(filepath.Join(root, "gone"), 0700); err != nil {
			t.Fatal(err)
		}
		return legacyignore.BatchResult{Decisions: []legacyignore.Decision{{Kind: legacyignore.RuleExclude, Line: 1}}}, nil
	})
	got, err := NewIgnoreScanner().MatchLegacyIgnoreBaseline(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "gone/deep"}, []LegacyCandidate{{Path: "gone/deep/file.mkv"}}, evaluator)
	if err != domain.ErrInventoryInvalidated || len(got.Result.Decisions) != 0 {
		t.Fatal("reappearing parent accepted", err)
	}
}
