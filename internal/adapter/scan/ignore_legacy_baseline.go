package scan

import (
	"context"
	ignoresource "github.com/MoYuanCN/Jelee/internal/adapter/media/ignore"
	"slices"

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
	o, err := domainLegacyBaselineObservation(d, last)
	if err != nil {
		return empty, err
	}
	return LegacyBaselineMatchBatch{Observation: o, SourceDirectory: last.Source.SourceDirectory, Result: result}, nil
}

func domainLegacyBaselineObservation(d domain.ScanDirectory, last ignoresource.LegacyBaselineObservation) (domain.LegacyIgnoreBaselineObservation, error) {
	o := domain.LegacyIgnoreBaselineObservation{Version: domain.LegacyIgnoreBaselineProofVersion, LookupDirectory: d.Path, Source: domain.LegacyIgnoreObservation{Version: domain.LegacyIgnoreProofVersion}}
	for _, p := range last.Source.DirectoryProofs() {
		if p.Version != o.Source.Version {
			return domain.LegacyIgnoreBaselineObservation{}, domain.ErrInventoryInvalidated
		}
		o.Source.Proofs = append(o.Source.Proofs, domain.LegacyIgnoreDirectoryProof{IgnoreDirectoryProof: domainIgnoreProof(d.RootID, p.DirectoryProof), Checked: p.Checked})
		o.Source.Directory = p.Directory
	}
	if last.MissingDirectory.MissingDirectory {
		o.MissingDirectory = domainIgnoreProof(d.RootID, last.MissingDirectory)
	}
	if domain.ValidateLegacyIgnoreBaselineObservation(o) != nil {
		return domain.LegacyIgnoreBaselineObservation{}, domain.ErrInventoryInvalidated
	}
	return o, nil
}

func (s *IgnoreScanner) ObserveLegacyIgnoreBaseline(ctx context.Context, d domain.ScanDirectory) (domain.LegacyIgnoreBaselineObservation, error) {
	if ctx == nil || s == nil || s.resolver == nil || !domain.ValidID(d.RootID) {
		return domain.LegacyIgnoreBaselineObservation{}, domain.ErrInvalid
	}
	native, err := s.resolver.ObserveLegacyBaseline(ctx, d.RootPath, d.Path)
	if err != nil {
		return domain.LegacyIgnoreBaselineObservation{}, ignoreWorkerError(ctx, err)
	}
	return domainLegacyBaselineObservation(d, native)
}

func (s *IgnoreScanner) ReobserveLegacyIgnoreBaseline(ctx context.Context, root string, retained domain.LegacyIgnoreBaselineObservation) (domain.LegacyIgnoreBaselineObservation, error) {
	if ctx == nil || domain.ValidateLegacyIgnoreBaselineObservation(retained) != nil {
		return domain.LegacyIgnoreBaselineObservation{}, domain.ErrInvalid
	}
	observed, err := s.ObserveLegacyIgnoreBaseline(ctx, domain.ScanDirectory{RootID: retained.Source.Proofs[0].RootID, RootPath: root, Path: retained.LookupDirectory})
	if err != nil {
		return domain.LegacyIgnoreBaselineObservation{}, err
	}
	if observed.Version != retained.Version || observed.LookupDirectory != retained.LookupDirectory || observed.MissingDirectory != retained.MissingDirectory || observed.Source.Version != retained.Source.Version || observed.Source.Directory != retained.Source.Directory || !slices.Equal(observed.Source.Proofs, retained.Source.Proofs) {
		return domain.LegacyIgnoreBaselineObservation{}, domain.ErrInventoryInvalidated
	}
	return observed, nil
}
