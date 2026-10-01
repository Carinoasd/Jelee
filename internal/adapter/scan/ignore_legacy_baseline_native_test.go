//go:build !race && (linux || windows)

package scan

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

func TestLegacyBaselineNativeHelper(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("*.mkv"))
	runner, err := process.NewIgnoreRunner(t.TempDir(), 2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewIgnoreScanner().MatchLegacyIgnoreBaseline(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "gone/deep"}, []LegacyCandidate{{Path: "gone/deep/movie.mkv"}, {Path: "gone/deep/plain.txt"}}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation.MissingDirectory.Directory != "gone" || got.Observation.Source.Directory != "." || got.SourceDirectory != "." || domain.ValidateLegacyIgnoreBaselineObservation(got.Observation) != nil {
		t.Fatal("missing lookup provenance")
	}
	if got.Result.Decisions[0].Kind != legacyignore.RuleExclude || got.Result.Decisions[0].Line != 1 || got.Result.Decisions[1].Kind != legacyignore.NoMatch {
		t.Fatal("unseen candidate matching")
	}
	if runner.Stats().Active != 0 {
		t.Fatal("helper leaked")
	}
}
