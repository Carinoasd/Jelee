package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// These internal ports persist preparation evidence only. A caller still needs
// policy, media/root scope and a fenced filesystem commit/resolution protocol.
type NFOWriteCommitFilesRepository interface {
	GetNFOWriteTask(context.Context, domain.JobLease, int) (domain.NFOWriteTask, error)
	BeginNFOWriteCommit(context.Context, domain.JobLease, int) (domain.NFOWriteCommitRecord, error)
	SaveNFOWriteCommitFilePlan(context.Context, domain.JobLease, int, string, domain.NFOWriteCommitFilePlan) (domain.NFOWriteCommitFilePlan, error)
	SaveNFOWriteCommitFilesReady(context.Context, domain.JobLease, int, string, domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error)
}
