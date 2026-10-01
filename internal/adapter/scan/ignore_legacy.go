package scan

import (
	"context"
	"slices"

	ignoresource "github.com/MoYuanCN/Jelee/internal/adapter/media/ignore"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ObserveLegacyIgnore binds the native nearest-source chain to a library root
// ID. It does not enumerate files, run the matcher or authorize a job mode.
func (s *IgnoreScanner) ObserveLegacyIgnore(ctx context.Context, d domain.ScanDirectory) (domain.LegacyIgnoreObservation, error) {
	if ctx == nil || s == nil || s.resolver == nil || !domain.ValidID(d.RootID) {
		return domain.LegacyIgnoreObservation{}, domain.ErrInvalid
	}
	native, err := s.resolver.ObserveLegacy(ctx, d.RootPath, d.Path)
	if err != nil {
		return domain.LegacyIgnoreObservation{}, ignoreWorkerError(ctx, err)
	}
	o := domain.LegacyIgnoreObservation{Version: domain.LegacyIgnoreProofVersion, Directory: d.Path}
	for _, p := range native.DirectoryProofs() {
		if p.Version != ignoresource.LegacySourceProofVersion || p.Version != o.Version {
			return domain.LegacyIgnoreObservation{}, domain.ErrInventoryInvalidated
		}
		o.Proofs = append(o.Proofs, domain.LegacyIgnoreDirectoryProof{IgnoreDirectoryProof: domainIgnoreProof(d.RootID, p.DirectoryProof), Checked: p.Checked})
	}
	if domain.ValidateLegacyIgnoreObservation(o) != nil {
		return domain.LegacyIgnoreObservation{}, domain.ErrInventoryInvalidated
	}
	return o, nil
}

// ReobserveLegacyIgnore compares the full original query, including checked
// absence below its chosen source. A newly present closer rule therefore
// invalidates the query, even if the old source itself has not changed.
// Success applies to this observation only, not to an entire job or lease.
func (s *IgnoreScanner) ReobserveLegacyIgnore(ctx context.Context, root string, retained domain.LegacyIgnoreObservation) (domain.LegacyIgnoreObservation, error) {
	if ctx == nil || domain.ValidateLegacyIgnoreObservation(retained) != nil {
		return domain.LegacyIgnoreObservation{}, domain.ErrInvalid
	}
	observed, err := s.ObserveLegacyIgnore(ctx, domain.ScanDirectory{RootID: retained.Proofs[0].RootID, RootPath: root, Path: retained.Directory})
	if err != nil {
		return domain.LegacyIgnoreObservation{}, err
	}
	if observed.Version != retained.Version || observed.Directory != retained.Directory || !slices.Equal(observed.Proofs, retained.Proofs) {
		return domain.LegacyIgnoreObservation{}, domain.ErrInventoryInvalidated
	}
	return observed, nil
}
