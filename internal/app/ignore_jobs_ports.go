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

// IgnoreExecutionRepository retains all source evidence under a live job lease.
// Root paths are trusted private values from the job's current library scope.
type IgnoreExecutionRepository interface {
	ReadIgnoreProgress(context.Context, domain.JobLease) (domain.IgnoreExecutionProgress, error)
	ReadIgnoreRequest(context.Context, domain.JobLease) (*domain.IgnoreRequest, error)
	ReadIgnoreRoot(context.Context, domain.JobLease, string) (string, error)
	NextIgnoreScanDirectory(context.Context, domain.JobLease) (domain.ScanDirectory, error)
	SaveIgnoreScanBatch(context.Context, domain.JobLease, domain.ScanDirectory, domain.IgnoreScanBatch) error
	RecordIgnoreProofs(context.Context, domain.JobLease, []domain.IgnoreDirectoryProof) error
	BeginIgnoreBaselineComparison(context.Context, domain.JobLease) error
	NextIgnoreBaselinePage(context.Context, domain.JobLease) (domain.IgnoreBaselinePage, error)
	CommitIgnoreBaselinePage(context.Context, domain.JobLease, domain.IgnoreBaselineToken, []domain.IgnoreBaselineDecision) error
	BeginIgnoreVerification(context.Context, domain.JobLease) error
	NextIgnoreVerificationPage(context.Context, domain.JobLease) (domain.IgnoreVerificationPage, error)
	CommitIgnoreVerificationPage(context.Context, domain.JobLease, domain.IgnoreVerificationToken, []domain.IgnoreDirectoryProof) error
	SealIgnoreVerification(context.Context, domain.JobLease) error
	FinishIgnoreJob(context.Context, domain.JobLease) error
}
