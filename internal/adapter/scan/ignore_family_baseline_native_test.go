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

func TestFamilyBaselineNativeHelper(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	for name, text := range map[string]string{
		".jeleeignore":   "!keep.mkv\ncustom/\n!allowed/\n!allowed/movie.mkv\n",
		".ignore":        "*.mkv\n",
		"hidden/.ignore": "", "hidden/.jeleeignore": "!movie.mkv\n",
		"invalid/.ignore": "[", "allowed/.ignore": "",
	} {
		writeScanFile(t, root, name, []byte(text))
	}
	runner, err := process.NewIgnoreRunner(t.TempDir(), 2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	s := NewFamilyIgnoreScanner(runner)
	for _, tt := range []struct {
		path, outcome, family, reason, matched string
		line                                   int
	}{
		{"keep.mkv", domain.IgnoreBaselineMissing, "", "", "", 0},
		{"allowed/movie.mkv", domain.IgnoreBaselineMissing, "", "", "", 0},
		{"hidden/movie.mkv", domain.IgnoreBaselineExcluded, domain.IgnoreFamilyLegacy, domain.IgnoreReasonBlank, "hidden", 0},
		{"invalid/movie.mkv", domain.IgnoreBaselineExcluded, domain.IgnoreFamilyLegacy, domain.IgnoreReasonInvalid, "invalid", 0},
		{"custom/movie.mkv", domain.IgnoreBaselineExcluded, domain.IgnoreFamilyCustom, domain.IgnoreReasonRule, "custom", 2},
		{"gone/deep/movie.mkv", domain.IgnoreBaselineExcluded, domain.IgnoreFamilyLegacy, domain.IgnoreReasonRule, "gone/deep/movie.mkv", 1},
		{"gone/deep/plain.txt", domain.IgnoreBaselineMissing, "", "", "", 0},
	} {
		t.Run(tt.path, func(t *testing.T) {
			got, err := s.EvaluateFamilyIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: tt.path}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive})
			d := got.Decision
			if err != nil || d.Outcome != tt.outcome || d.Family != tt.family || d.Reason != tt.reason || d.MatchedPath != tt.matched || d.RuleLine != tt.line {
				t.Fatalf("unexpected classification: outcome=%s family=%s reason=%s line=%d error=%v", d.Outcome, d.Family, d.Reason, d.RuleLine, err)
			}
			for _, o := range got.LegacyObservations {
				if domain.ValidateLegacyIgnoreBaselineObservation(o) != nil {
					t.Fatal("invalid retained evidence")
				}
			}
		})
	}
	if runner.Stats().Active != 0 {
		t.Fatal("helper survived classification")
	}
}
