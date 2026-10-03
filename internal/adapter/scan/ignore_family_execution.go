package scan

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.FamilyIgnoreScanner = (*FamilyIgnoreScanner)(nil)

func (s *FamilyIgnoreScanner) ReobserveIgnoreProof(ctx context.Context, root string, p domain.IgnoreDirectoryProof) (domain.IgnoreDirectoryProof, error) {
	return s.legacy.ReobserveIgnoreProof(ctx, root, p)
}
func (s *FamilyIgnoreScanner) ReobserveLegacyIgnore(ctx context.Context, root string, p domain.LegacyIgnoreObservation) (domain.LegacyIgnoreObservation, error) {
	return s.legacy.ReobserveLegacyIgnore(ctx, root, p)
}
func (s *FamilyIgnoreScanner) ReobserveLegacyIgnoreBaseline(ctx context.Context, root string, p domain.LegacyIgnoreBaselineObservation) (domain.LegacyIgnoreBaselineObservation, error) {
	return s.legacy.ReobserveLegacyIgnoreBaseline(ctx, root, p)
}
