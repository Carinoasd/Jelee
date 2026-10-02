package scan

import (
	"context"
	"path/filepath"
	"strings"

	ignoresource "github.com/MoYuanCN/Jelee/internal/adapter/media/ignore"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
)

// EvaluateFamilyIgnoreBaselineBatch reuses only pure text/path matching within
// one bounded page. Each candidate still takes the original native observation
// path, including source identity checks before and after the matcher call.
func (s *FamilyIgnoreScanner) EvaluateFamilyIgnoreBaselineBatch(ctx context.Context, root string, candidates []domain.IgnoreBaselineCandidate, intent domain.IgnoreIntent) ([]domain.FamilyBaselineEvaluation, error) {
	if ctx == nil || s == nil || s.custom == nil || s.legacy == nil || s.evaluator == nil || len(candidates) == 0 || len(candidates) > domain.ScanBatchMaxEntries {
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return nil, domain.ErrIgnoreUnavailable
	}
	groups := make(map[string][]string)
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		if !domain.ValidID(candidate.RootID) || candidate.RootID != candidates[0].RootID || !domain.ValidNFOObservationPath(candidate.Path) || seen[candidate.Path] {
			return nil, domain.ErrInvalid
		}
		if len(strings.Split(candidate.Path, "/")) > ignore.MaxPathComponents {
			return nil, domain.ErrScanLimit
		}
		seen[candidate.Path] = true
		full := filepath.ToSlash(filepath.Join(root, filepath.FromSlash(candidate.Path)))
		parent := filepath.ToSlash(filepath.Dir(full))
		groups[parent] = append(groups[parent], full)
	}
	ctx, cancel := context.WithTimeout(ctx, ignoresource.MaxDuration)
	defer cancel()
	memo := &familyMatchMemo{inner: s.evaluator, groups: groups, cache: make(map[familyMatchKey]familyMatchValue)}
	batched := *s                          // Hold one shared slot for the entire batch, including its memo.
	batched.slots = make(chan struct{}, 1) // Serial inner calls do not reacquire the shared slot.
	batched.evaluator = memo
	result := make([]domain.FamilyBaselineEvaluation, 0, len(candidates))
	for _, candidate := range candidates {
		value, err := batched.EvaluateFamilyIgnoreBaseline(ctx, root, candidate, intent)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

const familyMatchMemoBytes = 1 << 20
const familyMatchMemoEntries = 256

type familyMatchKey struct{ source, path string }
type familyMatchValue struct {
	decision     legacyignore.Decision
	invalidLines []int
}
type familyMatchMemo struct {
	inner  legacyBatchEvaluator
	groups map[string][]string
	cache  map[familyMatchKey]familyMatchValue
	bytes  int
}

func (m *familyMatchMemo) remember(key familyMatchKey, value familyMatchValue) {
	// Charge the full source and diagnostics for each key conservatively,
	// even when their backing storage is shared by the group.
	charge := len(key.source) + len(key.path) + 4*len(value.invalidLines) + 64
	if charge > familyMatchMemoBytes {
		return
	}
	if len(m.cache) >= familyMatchMemoEntries || m.bytes+charge > familyMatchMemoBytes {
		clear(m.cache)
		m.bytes = 0
	}
	m.cache[key] = value
	m.bytes += charge
}

func (m *familyMatchMemo) Evaluate(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return legacyignore.BatchResult{}, err
	}
	if len(b.Paths) != 1 {
		return m.inner.Evaluate(ctx, b)
	}
	key := familyMatchKey{b.Source, b.Paths[0]}
	if value, ok := m.cache[key]; ok {
		return legacyignore.BatchResult{Decisions: []legacyignore.Decision{value.decision}, InvalidLines: value.invalidLines}, nil
	}
	paths := b.Paths
	// Directory prefixes retain their trailing slash and are cached exactly.
	// Leaf groups only contain supplied path strings; no extra file is opened.
	if !strings.HasSuffix(key.path, "/") {
		if group := m.groups[filepath.ToSlash(filepath.Dir(key.path))]; len(group) > 0 {
			paths = group
		}
	}
	batch := legacyignore.Batch{Source: b.Source, Paths: paths}
	result, err := m.inner.Evaluate(ctx, batch)
	if err != nil {
		return legacyignore.BatchResult{}, err
	}
	if err = legacyignore.ValidateResult(ctx, batch, result); err != nil {
		return legacyignore.BatchResult{}, err
	}
	var selected legacyignore.Decision
	found := false
	for i, path := range paths {
		value := familyMatchValue{result.Decisions[i], result.InvalidLines}
		m.remember(familyMatchKey{b.Source, path}, value)
		if path == key.path {
			selected = value.decision
			found = true
		}
	}
	if !found {
		return legacyignore.BatchResult{}, domain.ErrInventoryInvalidated
	}
	return legacyignore.BatchResult{Decisions: []legacyignore.Decision{selected}, InvalidLines: result.InvalidLines}, nil
}
