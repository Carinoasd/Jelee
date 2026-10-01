//go:build !race && (linux || windows)

package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

// Run without race: production child memory limits are incompatible with the
// race runtime's virtual reservation. CI invokes this test explicitly.
func TestLegacyMatchNativeHelper(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	scratch := t.TempDir()
	// UTF-16LE: NUL is a real regex alternative, not a string terminator.
	text := "(\x00|a).mkv\n!keep.mkv"
	raw := []byte{0xff, 0xfe}
	for _, r := range text {
		raw = append(raw, byte(r), 0)
	}
	writeScanFile(t, root, ".ignore", raw)
	runner, err := process.NewIgnoreRunner(scratch, 1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewIgnoreScanner().MatchLegacyIgnore(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, []LegacyCandidate{{Path: "a.mkv"}, {Path: "b.mkv"}}, runner)
	if err != nil || len(got.Result.Decisions) != 2 || got.Result.Decisions[0].Kind != legacyignore.RuleExclude || got.Result.Decisions[1].Kind != legacyignore.NoMatch {
		t.Fatal("native decoded batch failed", err)
	}
	if runner.Stats().Started != 1 || runner.Stats().Active != 0 {
		t.Fatal("batch was not one reaped child")
	}
	files, err := os.ReadDir(scratch)
	if err != nil || len(files) != 0 {
		t.Fatal("helper temporary input retained", err)
	}
}
