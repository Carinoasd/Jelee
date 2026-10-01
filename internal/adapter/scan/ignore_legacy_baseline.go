package scan

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
)

type LegacyBaselineMatchBatch struct {
	Observation     domain.LegacyIgnoreBaselineObservation `json:"-"`
	SourceDirectory string                                 `json:"-"`
	Result          legacyignore.BatchResult               `json:"-"`
}

func (LegacyBaselineMatchBatch) String() string   { return "legacy baseline matches (data redacted)" }
func (LegacyBaselineMatchBatch) GoString() string { return "legacy baseline matches (data redacted)" }

// This evaluates rules for unseen candidates; missing inventory classification
// still requires repository coverage and a matching baseline snapshot.
func (s *IgnoreScanner) MatchLegacyIgnoreBaseline(ctx context.Context, d domain.ScanDirectory, candidates []LegacyCandidate, evaluator legacyBatchEvaluator) (LegacyBaselineMatchBatch, error) {
	empty := LegacyBaselineMatchBatch{}
	if ctx == nil || s == nil || s.resolver == nil || evaluator == nil || !domain.ValidID(d.RootID) || len(candidates) < 1 || len(candidates) > legacyignore.MaxBatchPaths {
		return empty, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	paths, err := legacyCandidatePaths(d, candidates)
	if err != nil {
		return empty, err
	}
	first, err := s.resolver.ObserveLegacyBaseline(ctx, d.RootPath, d.Path)
	if err != nil {
		return empty, ignoreWorkerError(ctx, err)
	}
	result, err := evaluateLegacySource(ctx, first.Source, paths, evaluator)
	if err != nil {
		return empty, err
	}
	last, err := s.resolver.ObserveLegacyBaseline(ctx, d.RootPath, d.Path)
	if err != nil {
		return empty, ignoreWorkerError(ctx, err)
	}
	if first.Token() != last.Token() {
		return empty, domain.ErrInventoryInvalidated
	}
	o := domain.LegacyIgnoreBaselineObservation{Version: domain.LegacyIgnoreBaselineProofVersion, LookupDirectory: d.Path, Source: domain.LegacyIgnoreObservation{Version: domain.LegacyIgnoreProofVersion}}
	for _, p := range last.Source.DirectoryProofs() {
		if p.Version != o.Source.Version {
			return empty, domain.ErrInventoryInvalidated
		}
		o.Source.Proofs = append(o.Source.Proofs, domain.LegacyIgnoreDirectoryProof{IgnoreDirectoryProof: domainIgnoreProof(d.RootID, p.DirectoryProof), Checked: p.Checked})
		o.Source.Directory = p.Directory
	}
	if last.MissingDirectory.MissingDirectory {
		o.MissingDirectory = domainIgnoreProof(d.RootID, last.MissingDirectory)
	}
	if domain.ValidateLegacyIgnoreBaselineObservation(o) != nil {
		return empty, domain.ErrInventoryInvalidated
	}
	return LegacyBaselineMatchBatch{Observation: o, SourceDirectory: last.Source.SourceDirectory, Result: result}, nil
}
