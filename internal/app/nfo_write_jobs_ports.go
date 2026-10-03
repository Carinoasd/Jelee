package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// This observation port grants no commit authority. Write admission and the
// commit journal must be connected before runtime can claim nfo_write jobs.
type NFOWriteTaskRepository interface {
	GetNFOWriteTask(context.Context, domain.JobLease, int) (domain.NFOWriteTask, error)
}
