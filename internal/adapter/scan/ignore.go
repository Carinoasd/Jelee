package scan

import (
	"context"
	"errors"
	"path"

	ignoresource "github.com/MoYuanCN/Jelee/internal/adapter/media/ignore"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

// IgnoreScanner adapts native observations without reopening media paths or
// deriving proof identities from a separate enumeration.
type IgnoreScanner struct{ resolver *ignoresource.Resolver }

var _ app.IgnoreInventoryScanner = (*IgnoreScanner)(nil)

func NewIgnoreScanner() *IgnoreScanner { return &IgnoreScanner{resolver: ignoresource.NewResolver()} }

func (s *IgnoreScanner) ScanIgnoreDirectory(ctx context.Context, d domain.ScanDirectory, intent domain.IgnoreIntent, emit func(domain.IgnoreScanBatch) error) error {
	if ctx == nil || s == nil || s.resolver == nil || emit == nil || !domain.ValidID(d.RootID) || intent.Mode == "" || domain.ValidateIgnoreIntent(intent) != nil {
		return domain.ErrIgnoreUnavailable
	}
	options := ignore.Options{}
	if intent.CaseMode == domain.IgnoreCaseASCIIInsensitive {
		options.Case = ignore.CaseASCIIInsensitive
	}
	var callbackErr error
	err := s.resolver.ScanDirectory(ctx, d.RootPath, d.Path, options, func(source ignoresource.ScanBatch) error {
		batch := domain.IgnoreScanBatch{Inventory: domain.ScanBatch{Done: source.Done, Skipped: source.Skipped}}
		for _, p := range source.Proofs {
			batch.Proofs = append(batch.Proofs, domainIgnoreProof(d.RootID, p))
		}
		if len(batch.Proofs) == 0 {
			return domain.ErrInventoryInvalidated
		}
		batch.HeldDirectoryIdentity = batch.Proofs[len(batch.Proofs)-1].Identity
		for _, entry := range source.Entries {
			entryKind := kind(entry.Path)
			if entry.Directory {
				entryKind = "directory"
			}
			if entry.Match.Outcome == ignore.Exclude {
				batch.Excluded = append(batch.Excluded, domain.IgnoreScanExclusion{Path: entry.Path, Kind: entryKind, RuleDirectory: path.Dir(entry.Match.Source), RuleLine: entry.Match.Line, MatchedPath: entry.Match.MatchedPath})
			} else if entry.Directory {
				batch.Inventory.Directories = append(batch.Inventory.Directories, entry.Path)
			} else {
				batch.Inventory.Entries = append(batch.Inventory.Entries, domain.InventoryEntry{RootID: d.RootID, Path: entry.Path, Kind: entryKind, Size: entry.Size, ModifiedUnixNano: entry.ModifiedUnixNano})
			}
		}
		callbackErr = emit(batch)
		return callbackErr
	})
	if callbackErr != nil {
		return callbackErr
	}
	if errors.Is(err, ignoresource.ErrChanged) {
		return domain.ErrInventoryInvalidated
	}
	if errors.Is(err, ignoresource.ErrLimit) || errors.Is(err, ignoresource.ErrWorkLimit) {
		return domain.ErrScanLimit
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return domain.ErrIgnoreUnavailable
	}
	return nil
}

var _ app.IgnoreBaselineObserver = (*IgnoreScanner)(nil)

func domainIgnoreProof(rootID string, p ignoresource.DirectoryProof) domain.IgnoreDirectoryProof {
	return domain.IgnoreDirectoryProof{RootID: rootID, Directory: p.Directory, ParentIdentity: p.ParentIdentity, Identity: p.Identity, MissingDirectory: p.MissingDirectory, RulePresent: p.RulePresent, RuleIdentity: p.RuleIdentity, RuleSize: p.RuleSize, RuleModifiedNano: p.RuleModifiedNano, RuleSHA256: p.RuleSHA256}
}

// EvaluateIgnoreBaseline does not itself establish that a file is missing.
// Only the repository's unseen page and completed directory coverage can do so.
func (s *IgnoreScanner) EvaluateIgnoreBaseline(ctx context.Context, root string, candidate domain.IgnoreBaselineCandidate, intent domain.IgnoreIntent) (domain.IgnoreBaselineDecision, []domain.IgnoreDirectoryProof, error) {
	empty := domain.IgnoreBaselineDecision{}
	if ctx == nil || s == nil || s.resolver == nil || !domain.ValidID(candidate.RootID) || !domain.ValidNFOObservationPath(candidate.Path) || intent.Mode == "" || domain.ValidateIgnoreIntent(intent) != nil {
		return empty, nil, domain.ErrInvalid
	}
	options := ignore.Options{}
	if intent.CaseMode == domain.IgnoreCaseASCIIInsensitive {
		options.Case = ignore.CaseASCIIInsensitive
	}
	observation, err := s.resolver.EvaluateBaseline(ctx, root, candidate.Path, ignore.File, options)
	if err != nil {
		return empty, nil, ignoreWorkerError(ctx, err)
	}
	proofs := make([]domain.IgnoreDirectoryProof, 0)
	for _, p := range observation.DirectoryProofs() {
		proofs = append(proofs, domainIgnoreProof(candidate.RootID, p))
	}
	if len(proofs) == 0 {
		return empty, nil, domain.ErrInventoryInvalidated
	}
	decision := domain.IgnoreBaselineDecision{RootID: candidate.RootID, Path: candidate.Path, Outcome: domain.IgnoreBaselineMissing}
	if observation.Match.Outcome == ignore.Exclude {
		decision.Outcome = domain.IgnoreBaselineExcluded
		decision.RuleDirectory = path.Dir(observation.Match.Source)
		decision.RuleLine = observation.Match.Line
		decision.MatchedPath = observation.Match.MatchedPath
	}
	if domain.ValidateIgnoreBaselineDecision(decision) != nil {
		return empty, nil, domain.ErrInventoryInvalidated
	}
	return decision, proofs, nil
}

// ReobserveIgnoreProof refuses an earlier missing ancestor rather than returning
// a fabricated absence for the requested descendant. Equality is checked again
// by the repository against its frozen verification page.
func (s *IgnoreScanner) ReobserveIgnoreProof(ctx context.Context, root string, retained domain.IgnoreDirectoryProof) (domain.IgnoreDirectoryProof, error) {
	if ctx == nil || s == nil || s.resolver == nil || domain.ValidateIgnoreDirectoryProof(retained) != nil {
		return domain.IgnoreDirectoryProof{}, domain.ErrInvalid
	}
	chain, err := s.resolver.ReobserveDirectory(ctx, root, retained.Directory)
	if err != nil {
		return domain.IgnoreDirectoryProof{}, ignoreWorkerError(ctx, err)
	}
	if len(chain) == 0 || chain[len(chain)-1].Directory != retained.Directory {
		return domain.IgnoreDirectoryProof{}, domain.ErrInventoryInvalidated
	}
	observed := domainIgnoreProof(retained.RootID, chain[len(chain)-1])
	if domain.ValidateIgnoreDirectoryProof(observed) != nil {
		return domain.IgnoreDirectoryProof{}, domain.ErrInventoryInvalidated
	}
	return observed, nil
}

func ignoreWorkerError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ignoresource.ErrChanged) {
		return domain.ErrInventoryInvalidated
	}
	if errors.Is(err, ignoresource.ErrLimit) || errors.Is(err, ignoresource.ErrWorkLimit) {
		return domain.ErrScanLimit
	}
	return domain.ErrIgnoreUnavailable
}
