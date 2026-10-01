package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ExecutionIgnoreRequestReader identifies the retained contract before dispatch.
type ExecutionIgnoreRequestReader interface {
	ReadExecutionIgnoreRequest(context.Context, domain.JobLease) (*domain.IgnoreRequest, error)
}

// FamilyIgnoreExecutionRepository keeps all three verification streams under
// the same lease and final publication seal.
type FamilyIgnoreExecutionRepository interface {
	ExecutionIgnoreRequestReader
	ReadFamilyIgnoreProgress(context.Context, domain.JobLease) (domain.IgnoreExecutionProgress, error)
	ReadFamilyIgnoreRoot(context.Context, domain.JobLease, string) (string, error)
	NextFamilyIgnoreScanDirectory(context.Context, domain.JobLease) (domain.ScanDirectory, error)
	SaveFamilyIgnoreScanBatch(context.Context, domain.JobLease, domain.ScanDirectory, domain.FamilyIgnoreScanBatch) error
	BeginFamilyIgnoreBaselineComparison(context.Context, domain.JobLease) error
	NextFamilyIgnoreBaselinePage(context.Context, domain.JobLease) (domain.IgnoreBaselinePage, error)
	CommitFamilyIgnoreBaselinePage(context.Context, domain.JobLease, domain.IgnoreBaselineToken, []domain.FamilyBaselineEvaluation) error
	BeginFamilyIgnoreVerification(context.Context, domain.JobLease) error
	NextFamilyIgnoreVerificationPage(context.Context, domain.JobLease) (domain.IgnoreVerificationPage, error)
	CommitFamilyIgnoreVerificationPage(context.Context, domain.JobLease, domain.IgnoreVerificationToken, []domain.IgnoreDirectoryProof) error
	NextLegacyIgnoreVerificationPage(context.Context, domain.JobLease) (domain.LegacyIgnoreVerificationPage, error)
	CommitLegacyIgnoreVerificationPage(context.Context, domain.JobLease, domain.LegacyIgnoreVerificationToken, []domain.LegacyIgnoreObservation) error
	NextLegacyIgnoreBaselineVerificationPage(context.Context, domain.JobLease) (domain.LegacyIgnoreBaselineVerificationPage, error)
	CommitLegacyIgnoreBaselineVerificationPage(context.Context, domain.JobLease, domain.LegacyIgnoreBaselineVerificationToken, []domain.LegacyIgnoreBaselineObservation) error
	SealFamilyIgnoreVerification(context.Context, domain.JobLease) error
	FinishFamilyIgnoreJob(context.Context, domain.JobLease) error
}

type FamilyIgnoreScanner interface {
	ScanFamilyIgnoreDirectory(context.Context, domain.ScanDirectory, domain.IgnoreIntent, func(domain.FamilyIgnoreScanBatch) error) error
	EvaluateFamilyIgnoreBaseline(context.Context, string, domain.IgnoreBaselineCandidate, domain.IgnoreIntent) (domain.FamilyBaselineEvaluation, error)
	ReobserveIgnoreProof(context.Context, string, domain.IgnoreDirectoryProof) (domain.IgnoreDirectoryProof, error)
	ReobserveLegacyIgnore(context.Context, string, domain.LegacyIgnoreObservation) (domain.LegacyIgnoreObservation, error)
	ReobserveLegacyIgnoreBaseline(context.Context, string, domain.LegacyIgnoreBaselineObservation) (domain.LegacyIgnoreBaselineObservation, error)
}
