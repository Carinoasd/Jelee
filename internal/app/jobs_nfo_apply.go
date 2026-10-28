package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOApplyJobRepository admits and reports nfo_apply jobs (G39.14).
type NFOApplyJobRepository interface {
	SubmitNFOApplyJob(ctx context.Context, actor domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error)
	GetNFOApplyState(ctx context.Context, actor domain.Actor, job string) (domain.NFOApplyJobState, error)
}

// NewJobsWithNFOApply lets base admit nfo_apply jobs through repository.
func NewJobsWithNFOApply(base *Jobs, repository NFOApplyJobRepository) (*Jobs, error) {
	if base == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	value := *base
	value.nfoApplyJobs = repository
	return &value, nil
}

// SubmitNFOApply queues a batch NFO apply of one library. The library must be
// read-only; otherwise the repository answers domain.ErrConflict.
func (j *Jobs) SubmitNFOApply(ctx context.Context, actor domain.Actor, library, key, priority string) (domain.Job, bool, error) {
	if ctx == nil || j == nil || !validTarget(actor, library) || !validKey(key) || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, false, err
	}
	if j.nfoApplyJobs == nil {
		return domain.Job{}, false, domain.ErrNFOReaderUnavailable
	}
	return j.nfoApplyJobs.SubmitNFOApplyJob(ctx, actor, library, key, priority, j.policy)
}

// NFOApplyState returns the progress of an nfo_apply job; any other job kind
// is domain.ErrNotFound.
func (j *Jobs) NFOApplyState(ctx context.Context, actor domain.Actor, job string) (domain.NFOApplyJobState, error) {
	if ctx == nil || j == nil || !validTarget(actor, job) {
		return domain.NFOApplyJobState{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOApplyJobState{}, err
	}
	if j.nfoApplyJobs == nil {
		return domain.NFOApplyJobState{}, domain.ErrNFOReaderUnavailable
	}
	return j.nfoApplyJobs.GetNFOApplyState(ctx, actor, job)
}
