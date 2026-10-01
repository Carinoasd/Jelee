//go:build linux || windows

package scan

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
)

type legacyEvaluatorFunc func(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error)

func (f legacyEvaluatorFunc) Evaluate(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
	return f(ctx, b)
}

func TestLegacyMatchDecodeAbsenceAndSourceChange(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, "child/movie.mkv", nil)
	s := NewIgnoreScanner()
	d := domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "child"}
	candidates := []LegacyCandidate{{Path: "child/movie.mkv"}}
	calls := 0
	evaluator := legacyEvaluatorFunc(func(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		calls++
		if b.Source != "*.mkv" || b.Paths[0] != filepath.ToSlash(filepath.Join(root, "child", "movie.mkv")) {
			t.Fatal("decode or full path lost")
		}
		return legacyignore.BatchResult{Decisions: []legacyignore.Decision{{Kind: legacyignore.RuleExclude, Line: 1}}}, nil
	})
	got, err := s.MatchLegacyIgnore(context.Background(), d, candidates, evaluator)
	if err != nil || calls != 0 || got.Result.Decisions[0].Kind != legacyignore.NoMatch {
		t.Fatal("absence invoked helper or excluded", err)
	}
	writeScanFile(t, root, ".ignore", []byte{0xff, 0xfe, '*', 0, '.', 0, 'm', 0, 'k', 0, 'v', 0})
	got, err = s.MatchLegacyIgnore(context.Background(), d, candidates, evaluator)
	if err != nil || calls != 1 || got.SourceDirectory != "." || got.Result.Decisions[0].Kind != legacyignore.RuleExclude {
		t.Fatal("decoded evaluation", err)
	}
	changed := legacyEvaluatorFunc(func(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		writeScanFile(t, root, "child/.ignore", nil)
		return evaluator.Evaluate(ctx, b)
	})
	if got, err = s.MatchLegacyIgnore(context.Background(), d, candidates, changed); err != domain.ErrInventoryInvalidated || len(got.Result.Decisions) != 0 {
		t.Fatal("changed lookup published", err)
	}
}

func TestLegacyMatchDirectoryOwnRulesAndFailure(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, "child/.ignore", nil)
	s := NewIgnoreScanner()
	d := domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "child"}
	evaluator := legacyEvaluatorFunc(func(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		if b.Source != "" || b.Paths[0] != filepath.ToSlash(filepath.Join(root, "child"))+"/" {
			t.Fatal("directory must use own empty rule")
		}
		return legacyignore.BatchResult{Decisions: []legacyignore.Decision{{Kind: legacyignore.BlankExclude}}}, nil
	})
	got, err := s.MatchLegacyIgnore(context.Background(), d, []LegacyCandidate{{Path: "child", Directory: true}}, evaluator)
	if err != nil || got.SourceDirectory != "child" || got.Result.Decisions[0].Kind != legacyignore.BlankExclude {
		t.Fatal("directory own rule", err)
	}
	for _, bad := range []legacyEvaluatorFunc{
		func(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error) {
			return legacyignore.BatchResult{}, errors.New("private failure")
		},
		func(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error) {
			return legacyignore.BatchResult{}, nil
		},
	} {
		if got, err = s.MatchLegacyIgnore(context.Background(), d, []LegacyCandidate{{Path: "child", Directory: true}}, bad); err != domain.ErrIgnoreUnavailable || len(got.Result.Decisions) != 0 {
			t.Fatal("helper failure published", err)
		}
	}
}
