package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOWriteBatchMax is the most preparations one nfo_write job may carry.
const NFOWriteBatchMax = 100

// NewJobsWithNFOWrite lets base admit nfo_write jobs through repository.
func NewJobsWithNFOWrite(base *Jobs, repository NFOWriteJobRepository) (*Jobs, error) {
	if base == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	value := *base
	value.nfoWriteJobs = repository
	return &value, nil
}

// SubmitNFOWrite queues the actor's prepared NFO edits as one nfo_write job.
// Without a write repository every library counts as not writable.
func (j *Jobs) SubmitNFOWrite(ctx context.Context, actor domain.Actor, library, key, priority string, preparations []string) (domain.Job, bool, error) {
	if ctx == nil || j == nil || !validTarget(actor, library) || !validKey(key) || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) || len(preparations) < 1 || len(preparations) > NFOWriteBatchMax {
		return domain.Job{}, false, domain.ErrInvalid
	}
	seen := make(map[string]bool, len(preparations))
	for _, id := range preparations {
		if !domain.ValidID(id) || seen[id] {
			return domain.Job{}, false, domain.ErrInvalid
		}
		seen[id] = true
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, false, err
	}
	if j.nfoWriteJobs == nil {
		return domain.Job{}, false, domain.ErrNFOWriteDisabled
	}
	return j.nfoWriteJobs.SubmitNFOWriteJob(ctx, actor, library, key, priority, append([]string(nil), preparations...), j.policy)
}
