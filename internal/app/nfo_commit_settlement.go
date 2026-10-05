package app

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOWriteCommitSettlementRepository persists settlement phases of a prepared
// token. Each save is a short fenced transaction under the original or recovery
// lease; an error can mean an unknown commit outcome, so callers re-read.
type NFOWriteCommitSettlementRepository interface {
	NFOWriteCommitAttemptStageRepository
	GetNFOWriteCommitSettlement(context.Context, domain.JobLease, int, string) (domain.NFOWriteCommitSettlement, error)
	SaveNFOWriteCommitSettlement(context.Context, domain.JobLease, int, string, uint8, domain.NFOWriteCommitSettlementPhase) (domain.NFOWriteCommitSettlement, error)
}

// NFOWriteExecutionRepository is the durable side of the NFO write worker:
// entry observation, fenced job completion and the recovery lease lifecycle.
// Completion and recovery resolution never authorize a filesystem change.
type NFOWriteExecutionRepository interface {
	NFOWriteCommitSettlementRepository
	ListNFOWriteCommitEntries(context.Context, domain.JobLease) ([]domain.NFOWriteCommitEntryState, error)
	// FinishNFOWriteJob stops a running job. It reports whether the job was
	// resolved; an unresolved job awaits the recovery lease.
	FinishNFOWriteJob(context.Context, domain.JobLease, string, string) (bool, error)
	ListNFOWriteCommitRecoveries(context.Context, int) ([]string, error)
	AcquireNFOWriteCommitRecovery(context.Context, string, string, time.Duration) (domain.JobLease, error)
	RenewNFOWriteCommitRecovery(context.Context, domain.JobLease, time.Duration) (domain.JobLease, error)
	// CompleteNFOWriteCommitRecovery resolves a stopped job and returns its
	// final state. A failed job whose every entry was replaced succeeds.
	CompleteNFOWriteCommitRecovery(context.Context, domain.JobLease) (string, error)
}

// NFOWriteJobRepository admits prepared intents as one queued nfo_write job.
type NFOWriteJobRepository interface {
	SubmitNFOWriteJob(context.Context, domain.Actor, string, string, string, []string, domain.JobPolicy) (domain.Job, bool, error)
}
