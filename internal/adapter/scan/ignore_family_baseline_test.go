//go:build linux || windows

package scan

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
)

func TestFamilyBaselineCancellationReleasesWholeOperationSlots(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("*"))
	entered := make(chan struct{}, 2)
	evaluator := legacyEvaluatorFunc(func(ctx context.Context, _ legacyignore.Batch) (legacyignore.BatchResult, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
			t.Error("helper missing operation deadline")
		}
		entered <- struct{}{}
		<-ctx.Done()
		return legacyignore.BatchResult{}, ctx.Err()
	})
	s := NewFamilyIgnoreScanner(evaluator)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	intent := domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}
	candidate := domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "movie.mkv"}
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := s.EvaluateFamilyIgnoreBaseline(ctx, root, candidate, intent); done <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("operations did not reach evaluator")
		}
	}
	if got, err := s.EvaluateFamilyIgnoreBaseline(context.Background(), root, candidate, intent); err != domain.ErrIgnoreUnavailable || got.Decision.Path != "" {
		t.Fatal("third operation exceeded slots", err)
	}
	cancel()
	for range 2 {
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Fatal("cancellation lost", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("operation did not cancel")
		}
	}
	if len(s.slots) != 0 {
		t.Fatal("whole-operation slot leaked")
	}
}

func TestFamilyBaselineRejectsChangedLegacyAcrossLookups(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("# initial"))
	writeScanFile(t, root, "child/placeholder", nil)
	calls := 0
	evaluator := legacyEvaluatorFunc(func(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error) {
		calls++
		if calls == 2 {
			writeScanFile(t, root, ".ignore", []byte("# changed"))
		}
		return legacyignore.BatchResult{Decisions: []legacyignore.Decision{{Kind: legacyignore.NoMatch}}}, nil
	})
	got, err := NewFamilyIgnoreScanner(evaluator).EvaluateFamilyIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "child/movie.mkv"}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive})
	if err != domain.ErrInventoryInvalidated || got.Decision.Path != "" || calls != 2 {
		t.Fatal("changed legacy evidence accepted", err)
	}
}

func TestFamilyBaselineAncestorCannotBeResurrected(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, "hidden/.ignore", nil)
	writeScanFile(t, root, "hidden/.jeleeignore", []byte("!movie.mkv\n"))
	calls := 0
	evaluator := legacyEvaluatorFunc(func(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		calls++
		if b.Source != "" || len(b.Paths) != 1 || b.Paths[0] != filepath.ToSlash(filepath.Join(root, "hidden"))+"/" {
			t.Fatal("did not classify the ancestor first")
		}
		return legacyignore.BatchResult{Decisions: []legacyignore.Decision{{Kind: legacyignore.BlankExclude}}}, nil
	})
	got, err := NewFamilyIgnoreScanner(evaluator).EvaluateFamilyIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "hidden/movie.mkv"}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive})
	if err != nil || got.Decision.Outcome != domain.IgnoreBaselineExcluded || got.Decision.MatchedPath != "hidden" || got.Decision.RuleDirectory != "hidden" || got.Decision.Reason != domain.IgnoreReasonBlank || calls != 1 {
		t.Fatal("ancestor exclusion lost", err)
	}
	if len(got.CustomProofs) != 1 || len(got.LegacyObservations) != 1 {
		t.Fatal("entered excluded subtree for custom rules")
	}
}

func TestFamilyBaselineExplicitIncludeBypassesLegacy(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".jeleeignore", []byte("!hidden/\n!hidden/movie.mkv\n"))
	writeScanFile(t, root, "hidden/.ignore", nil)
	evaluator := legacyEvaluatorFunc(func(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error) {
		t.Fatal("explicit include consulted legacy")
		return legacyignore.BatchResult{}, nil
	})
	got, err := NewFamilyIgnoreScanner(evaluator).EvaluateFamilyIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "hidden/movie.mkv"}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive})
	if err != nil || got.Decision.Outcome != domain.IgnoreBaselineMissing || len(got.LegacyObservations) != 0 {
		t.Fatal("explicit include failed", err)
	}
}

func TestFamilyBaselineMissingParentRetainsBoundary(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	evaluator := legacyEvaluatorFunc(func(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error) {
		t.Fatal("absent source called evaluator")
		return legacyignore.BatchResult{}, nil
	})
	got, err := NewFamilyIgnoreScanner(evaluator).EvaluateFamilyIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "gone/deep/movie.mkv"}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive})
	if err != nil || got.Decision.Outcome != domain.IgnoreBaselineMissing || len(got.CustomProofs) != 2 || len(got.LegacyObservations) != 3 {
		t.Fatal("missing boundary classification failed", err)
	}
	for _, o := range got.LegacyObservations {
		if len(o.Source.Proofs) != 1 || o.MissingDirectory.Directory != "gone" {
			t.Fatal("fabricated descendant identity")
		}
	}
}

func TestFamilyBaselineRejectsCustomChangeBetweenAncestors(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("# comment"))
	writeScanFile(t, root, "child/placeholder", nil)
	evaluator := legacyEvaluatorFunc(func(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error) {
		writeScanFile(t, root, ".jeleeignore", []byte("!child/movie.mkv\n"))
		return legacyignore.BatchResult{Decisions: []legacyignore.Decision{{Kind: legacyignore.NoMatch}}}, nil
	})
	got, err := NewFamilyIgnoreScanner(evaluator).EvaluateFamilyIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "child/movie.mkv"}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive})
	if err != domain.ErrInventoryInvalidated || got.Decision.Path != "" || len(got.CustomProofs) != 0 {
		t.Fatal("mixed custom snapshot accepted", err)
	}
}
