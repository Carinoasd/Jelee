package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Each operation commits a short fenced transaction. The records are private
// data; neither allocation nor a first native identity grants filesystem power.
type NFOWriteCommitAttemptRepository interface {
	ReserveNFOWriteCommitAttempts(context.Context, domain.JobLease, int, string, domain.NFOWriteCommitAttemptReservation) (domain.NFOWriteCommitAttemptReservation, error)
	AllocateNFOWriteCommitAttempt(context.Context, domain.JobLease, int, string, uint8) (uint8, error)
	GetNFOWriteCommitAttempts(context.Context, domain.JobLease, int, string) (domain.NFOWriteCommitAttempts, error)
	SaveNFOWriteCommitAttemptCheckpoint(context.Context, domain.JobLease, int, string, uint8, domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error)
	SaveNFOWriteCommitAttemptReady(context.Context, domain.JobLease, int, string, uint8, domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error)
}

// Stage selects persisted attempts only through a checkpoint-capable repository.
type NFOWriteCommitAttemptStageRepository interface {
	NFOWriteCommitCheckpointRepository
	NFOWriteCommitAttemptRepository
}
