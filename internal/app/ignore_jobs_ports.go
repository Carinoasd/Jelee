package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// IgnoreInventoryScanner binds each batch to the same native directory handle
// used for enumeration. The repository must retain proofs and fence writes.
type IgnoreInventoryScanner interface {
	ScanIgnoreDirectory(context.Context, domain.ScanDirectory, domain.IgnoreIntent, func(domain.IgnoreScanBatch) error) error
}

// IgnoreBaselineObserver evaluates only candidates already proven unseen by the
// repository. Included means provisionally missing; coverage remains a DB check.
type IgnoreBaselineObserver interface {
	EvaluateIgnoreBaseline(context.Context, string, domain.IgnoreBaselineCandidate, domain.IgnoreIntent) (domain.IgnoreBaselineDecision, []domain.IgnoreDirectoryProof, error)
	ReobserveIgnoreProof(context.Context, string, domain.IgnoreDirectoryProof) (domain.IgnoreDirectoryProof, error)
}
