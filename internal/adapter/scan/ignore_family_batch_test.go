//go:build linux || windows

package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
)

type familyBatchPort interface {
	EvaluateFamilyIgnoreBaselineBatch(context.Context, string, []domain.IgnoreBaselineCandidate, domain.IgnoreIntent) ([]domain.FamilyBaselineEvaluation, error)
}

func TestFamilyBaselineBatchCancellationAndSlots(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("# baseline"))
	entered := make(chan struct{}, 2)
	s := NewFamilyIgnoreScanner(legacyEvaluatorFunc(func(ctx context.Context, _ legacyignore.Batch) (legacyignore.BatchResult, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return legacyignore.BatchResult{}, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	candidates := []domain.IgnoreBaselineCandidate{{RootID: testRootID, Path: "movie.mkv"}}
	intent := domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := s.EvaluateFamilyIgnoreBaselineBatch(ctx, root, candidates, intent); done <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("batch did not enter evaluator")
		}
	}
	if _, err := s.EvaluateFamilyIgnoreBaselineBatch(ctx, root, candidates, intent); !errors.Is(err, domain.ErrIgnoreUnavailable) {
		t.Fatal("third batch exceeded whole-operation admission", err)
	}
	if _, err := s.EvaluateFamilyIgnoreBaseline(ctx, root, candidates[0], intent); !errors.Is(err, domain.ErrIgnoreUnavailable) {
		t.Fatal("ordinary call bypassed batch admission", err)
	}
	cancel()
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("batch did not cancel")
		}
	}
	if len(s.slots) != 0 {
		t.Fatal("batch retained admission slot")
	}
}

func TestFamilyBaselineBatchCachedMatchStillObservesSource(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("# baseline"))
	calls := 0
	inner := legacyEvaluatorFunc(func(_ context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		calls++
		return legacyignore.BatchResult{Decisions: make([]legacyignore.Decision, len(b.Paths))}, nil
	})
	a := filepath.ToSlash(filepath.Join(root, "a.mkv"))
	b := filepath.ToSlash(filepath.Join(root, "b.mkv"))
	memo := &familyMatchMemo{inner: inner, groups: map[string][]string{filepath.ToSlash(root): {a, b}}, cache: make(map[familyMatchKey]familyMatchValue)}
	s := NewFamilyIgnoreScanner(memo)
	intent := domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}
	first, err := s.EvaluateFamilyIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "a.mkv"}, intent)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 0)
	if err = os.Chtimes(filepath.Join(root, ".ignore"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	second, err := s.EvaluateFamilyIgnoreBaseline(context.Background(), root, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "b.mkv"}, intent)
	if err != nil || calls != 1 || reflect.DeepEqual(first.LegacyObservations, second.LegacyObservations) {
		t.Fatal("pure cache reused stale native source evidence", calls, err)
	}
}

func TestFamilyBaselineBatchMemoBoundsAndSourceKey(t *testing.T) {
	calls := 0
	inner := legacyEvaluatorFunc(func(_ context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		calls++
		return legacyignore.BatchResult{Decisions: make([]legacyignore.Decision, len(b.Paths))}, nil
	})
	memo := &familyMatchMemo{inner: inner, cache: make(map[familyMatchKey]familyMatchValue)}
	ctx := context.Background()
	for _, source := range []string{"# a", "# a", "# b"} {
		if _, err := memo.Evaluate(ctx, legacyignore.Batch{Source: source, Paths: []string{"/movie.mkv"}}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("changed source reused matching result", calls)
	}
	for i := 0; i < 1000; i++ {
		if _, err := memo.Evaluate(ctx, legacyignore.Batch{Source: strings.Repeat("#"+strings.Repeat("x", 1023)+"\n", 8), Paths: []string{fmt.Sprintf("/file-%04d", i)}}); err != nil {
			t.Fatal(err)
		}
		if memo.bytes > familyMatchMemoBytes || len(memo.cache) > familyMatchMemoEntries {
			t.Fatal("memo exceeded memory or entry budget")
		}
	}
}

func TestFamilyBaselineBatchRejectsInvalidOrChangingEvidence(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("# before"))
	intent := domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}
	c := domain.IgnoreBaselineCandidate{RootID: testRootID, Path: "a.mkv"}
	s := NewFamilyIgnoreScanner(legacyEvaluatorFunc(func(_ context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		writeScanFile(t, root, ".ignore", []byte("# changed"))
		return legacyignore.BatchResult{Decisions: make([]legacyignore.Decision, len(b.Paths))}, nil
	}))
	for _, candidates := range [][]domain.IgnoreBaselineCandidate{nil, make([]domain.IgnoreBaselineCandidate, 129), {c, c}, {{RootID: testRootID, Path: "../outside"}}} {
		if _, err := s.EvaluateFamilyIgnoreBaselineBatch(context.Background(), root, candidates, intent); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("invalid batch accepted", err)
		}
	}
	if value, err := s.EvaluateFamilyIgnoreBaselineBatch(context.Background(), root, []domain.IgnoreBaselineCandidate{c}, intent); !errors.Is(err, domain.ErrInventoryInvalidated) || value != nil {
		t.Fatal("changed source accepted by batching", err)
	}
}

func TestFamilyBaselineBatchCombinesPureMatching(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("# baseline\n"))
	writeScanFile(t, root, "child/placeholder", nil)
	calls, maxPaths := 0, 0
	evaluator := legacyEvaluatorFunc(func(_ context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		calls++
		maxPaths = max(maxPaths, len(b.Paths))
		return legacyignore.BatchResult{Decisions: make([]legacyignore.Decision, len(b.Paths))}, nil
	})
	s := NewFamilyIgnoreScanner(evaluator)
	batch, ok := any(s).(familyBatchPort)
	if !ok {
		t.Fatal("family scanner has no bounded batch evaluation")
	}
	intent := domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}
	var candidates []domain.IgnoreBaselineCandidate
	for i := 0; i < 128; i++ {
		candidates = append(candidates, domain.IgnoreBaselineCandidate{RootID: testRootID, Path: fmt.Sprintf("child/movie-%03d.mkv", i)})
	}
	got, err := batch.EvaluateFamilyIgnoreBaselineBatch(context.Background(), root, candidates, intent)
	if err != nil || len(got) != 128 || calls != 2 || maxPaths != 128 {
		t.Fatal("matching was not bounded and combined", len(got), calls, maxPaths, err)
	}
	for i, candidate := range candidates {
		want, err := s.EvaluateFamilyIgnoreBaseline(context.Background(), root, candidate, intent)
		if err != nil || !reflect.DeepEqual(got[i], want) {
			t.Fatal("batch changed decision or source evidence", i, err)
		}
	}
}

func TestFamilyBaselineBatchKeepsPerCandidateDeadline(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("# baseline\n"))
	for _, directory := range []string{"first", "second"} {
		writeScanFile(t, root, directory+"/placeholder", nil)
	}
	var deadlines []time.Time
	s := NewFamilyIgnoreScanner(legacyEvaluatorFunc(func(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		if len(b.Paths) == 1 && !strings.HasSuffix(b.Paths[0], "/") {
			deadline, ok := ctx.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining <= 0 || remaining > 30*time.Second {
				t.Fatal("candidate helper lacks its bounded deadline")
			}
			deadlines = append(deadlines, deadline)
		}
		return legacyignore.BatchResult{Decisions: make([]legacyignore.Decision, len(b.Paths))}, nil
	}))
	candidates := []domain.IgnoreBaselineCandidate{
		{RootID: testRootID, Path: "first/movie.mkv"},
		{RootID: testRootID, Path: "second/movie.mkv"},
	}
	intent := domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}
	values, err := s.EvaluateFamilyIgnoreBaselineBatch(context.Background(), root, candidates, intent)
	if err != nil || len(values) != len(candidates) || len(deadlines) != len(candidates) {
		t.Fatal("both candidates must reach their own helper evaluation", len(values), len(deadlines), err)
	}
	if !deadlines[1].After(deadlines[0]) {
		t.Fatal("second candidate inherited the first candidate's deadline")
	}
}

func TestFamilyBaselineBatchMemoAccountsBackingStorage(t *testing.T) {
	memo := &familyMatchMemo{cache: make(map[familyMatchKey]familyMatchValue)}
	for i := 0; i < 1000; i++ {
		key := familyMatchKey{source: fmt.Sprintf("source-%04d", i), path: fmt.Sprintf("/file-%04d", i)}
		// Distinct allocations with spare capacity model retained decoder storage.
		value := familyMatchValue{invalidLines: make([]int, 2048, 4096)}
		memo.remember(key, value)
		if _, ok := memo.cache[key]; !ok {
			t.Fatal("an entry within the memory budget was not retained")
		}
		retained := 0
		for cachedKey, cachedValue := range memo.cache {
			retained += len(cachedKey.source) + len(cachedKey.path) + cap(cachedValue.invalidLines)*(strconv.IntSize/8) + 64
		}
		if retained > memo.bytes || memo.bytes > familyMatchMemoBytes || len(memo.cache) > familyMatchMemoEntries {
			t.Fatal("memo undercounted retained backing storage or exceeded its budget", retained, memo.bytes, len(memo.cache))
		}
		before := memo.bytes
		memo.remember(key, value)
		if memo.bytes != before {
			t.Fatal("remembering an existing pure result charged it again")
		}
	}
}
