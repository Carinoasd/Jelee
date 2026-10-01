package scan

import (
	"context"
	"path"
	"slices"

	ignoresource "github.com/MoYuanCN/Jelee/internal/adapter/media/ignore"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
)

// Separate source resolvers avoid consuming the enumeration resolver's own
// slots recursively. At most two enumerations and two legacy reads coexist.
type FamilyIgnoreScanner struct {
	custom    *ignoresource.Resolver
	legacy    *IgnoreScanner
	evaluator legacyBatchEvaluator
	slots     chan struct{}
}

func NewFamilyIgnoreScanner(evaluator legacyBatchEvaluator) *FamilyIgnoreScanner {
	return &FamilyIgnoreScanner{custom: ignoresource.NewResolver(), legacy: NewIgnoreScanner(), evaluator: evaluator, slots: make(chan struct{}, 2)}
}

func (s *FamilyIgnoreScanner) ScanFamilyIgnoreDirectory(ctx context.Context, d domain.ScanDirectory, intent domain.IgnoreIntent, emit func(domain.FamilyIgnoreScanBatch) error) error {
	if ctx == nil || s == nil || s.custom == nil || s.legacy == nil || s.evaluator == nil || emit == nil || !domain.ValidID(d.RootID) || intent.Mode != domain.IgnoreModeFamily || (intent.CaseMode != domain.IgnoreCaseSensitive && intent.CaseMode != domain.IgnoreCaseASCIIInsensitive) {
		return domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return domain.ErrIgnoreUnavailable
	}
	// The same deadline must reach nested matching and its child process;
	// the custom resolver's private child context is not passed to callbacks.
	ctx, cancel := context.WithTimeout(ctx, ignoresource.MaxDuration)
	defer cancel()
	options := ignore.Options{}
	if intent.CaseMode == domain.IgnoreCaseASCIIInsensitive {
		options.Case = ignore.CaseASCIIInsensitive
	}
	var retained *domain.LegacyIgnoreObservation
	var callbackErr error
	err := s.custom.ScanDirectoryWithChildIdentities(ctx, d.RootPath, d.Path, options, func(source ignoresource.ScanBatch) error {
		batch, e := s.familyBatch(ctx, d, source, retained)
		if e != nil {
			callbackErr = e
			return e
		}
		if retained == nil {
			snapshot := batch.LegacyObservations[0]
			snapshot.Proofs = slices.Clone(snapshot.Proofs)
			retained = &snapshot
		}
		callbackErr = emit(batch)
		return callbackErr
	})
	if callbackErr != nil {
		return callbackErr
	}
	if err != nil {
		return ignoreWorkerError(ctx, err)
	}
	return nil
}

func (s *FamilyIgnoreScanner) familyBatch(ctx context.Context, d domain.ScanDirectory, source ignoresource.ScanBatch, retained *domain.LegacyIgnoreObservation) (domain.FamilyIgnoreScanBatch, error) {
	empty := domain.FamilyIgnoreScanBatch{}
	if len(source.Proofs) == 0 || len(source.Entries) > legacyignore.MaxBatchPaths {
		return empty, domain.ErrInventoryInvalidated
	}
	batch := domain.FamilyIgnoreScanBatch{Inventory: domain.ScanBatch{Done: source.Done, Skipped: source.Skipped}}
	for _, p := range source.Proofs {
		batch.CustomProofs = append(batch.CustomProofs, domainIgnoreProof(d.RootID, p))
	}
	tail := batch.CustomProofs[len(batch.CustomProofs)-1]
	if tail.Directory != d.Path {
		return empty, domain.ErrInventoryInvalidated
	}
	batch.HeldDirectoryIdentity = tail.Identity
	base, err := s.legacy.ObserveLegacyIgnore(ctx, d)
	if err != nil {
		return empty, err
	}
	if !familyDirectoryChainMatches(batch.CustomProofs, base.Proofs) || retained != nil && !slices.Equal(base.Proofs, retained.Proofs) {
		return empty, domain.ErrInventoryInvalidated
	}
	batch.LegacyObservations = []domain.LegacyIgnoreObservation{base}
	decisions := make(map[int]legacyignore.Decision)
	sources := make(map[int]string)
	var files []LegacyCandidate
	var indices []int
	for i, e := range source.Entries {
		if e.Match.Outcome == ignore.Unmatched && !e.Directory {
			files = append(files, LegacyCandidate{Path: e.Path})
			indices = append(indices, i)
		}
	}
	if len(files) > 0 {
		matched, e := s.legacy.MatchLegacyIgnore(ctx, d, files, s.evaluator)
		if e != nil {
			return empty, e
		}
		if !slices.Equal(matched.Observation.Proofs, base.Proofs) {
			return empty, domain.ErrInventoryInvalidated
		}
		for j, i := range indices {
			decisions[i] = matched.Result.Decisions[j]
			sources[i] = matched.SourceDirectory
		}
	}
	for i, e := range source.Entries {
		entryKind := kind(e.Path)
		if e.Directory {
			entryKind = "directory"
		}
		if e.Match.Outcome == ignore.Exclude {
			batch.Excluded = append(batch.Excluded, domain.FamilyIgnoreExclusion{Path: e.Path, Kind: entryKind, Family: domain.IgnoreFamilyCustom, Reason: domain.IgnoreReasonRule, RuleDirectory: path.Dir(e.Match.Source), RuleLine: e.Match.Line, MatchedPath: e.Match.MatchedPath})
			continue
		}
		if e.Match.Outcome == ignore.Unmatched && e.Directory {
			child := d
			child.Path = e.Path
			matched, err := s.legacy.MatchLegacyIgnore(ctx, child, []LegacyCandidate{{Path: e.Path, Directory: true}}, s.evaluator)
			if err != nil {
				return empty, err
			}
			proofs := matched.Observation.Proofs
			leaf := proofs[len(proofs)-1]
			if leaf.Identity != e.Identity || leaf.ParentIdentity != tail.Identity || !familyDirectoryChainMatches(batch.CustomProofs, proofs[:len(proofs)-1]) {
				return empty, domain.ErrInventoryInvalidated
			}
			batch.LegacyObservations = append(batch.LegacyObservations, matched.Observation)
			decisions[i] = matched.Result.Decisions[0]
			sources[i] = matched.SourceDirectory
		}
		if decision, ok := decisions[i]; ok && (decision.Kind == legacyignore.RuleExclude || decision.Kind == legacyignore.BlankExclude || decision.Kind == legacyignore.InvalidSourceExclude) {
			reason := domain.IgnoreReasonRule
			if decision.Kind == legacyignore.BlankExclude {
				reason = domain.IgnoreReasonBlank
			}
			if decision.Kind == legacyignore.InvalidSourceExclude {
				reason = domain.IgnoreReasonInvalid
			}
			batch.Excluded = append(batch.Excluded, domain.FamilyIgnoreExclusion{Path: e.Path, Kind: entryKind, Family: domain.IgnoreFamilyLegacy, Reason: reason, RuleDirectory: sources[i], RuleLine: decision.Line, MatchedPath: e.Path})
			continue
		}
		if e.Directory {
			batch.Inventory.Directories = append(batch.Inventory.Directories, e.Path)
		} else {
			batch.Inventory.Entries = append(batch.Inventory.Entries, domain.InventoryEntry{RootID: d.RootID, Path: e.Path, Kind: entryKind, Size: e.Size, ModifiedUnixNano: e.ModifiedUnixNano})
		}
	}
	return batch, nil
}

func familyDirectoryChainMatches(custom []domain.IgnoreDirectoryProof, legacy []domain.LegacyIgnoreDirectoryProof) bool {
	if len(custom) != len(legacy) {
		return false
	}
	for i, p := range custom {
		q := legacy[i]
		if p.RootID != q.RootID || p.Directory != q.Directory || p.Identity != q.Identity || p.ParentIdentity != q.ParentIdentity {
			return false
		}
	}
	return true
}
