package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ImageRefreshJobRepository admits and reports image_refresh jobs (G40.9,
// G40.10).
type ImageRefreshJobRepository interface {
	SubmitImageRefreshJob(ctx context.Context, actor domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error)
	GetImageRefreshState(ctx context.Context, actor domain.Actor, job string) (domain.ImageRefreshJobState, error)
}

// NewJobsWithImageRefresh lets base report image_refresh jobs through
// repository. admit is true only where remote fetching and the persistent
// image store are configured; otherwise a submission is
// domain.ErrImageUnavailable while the progress of earlier jobs stays
// readable.
func NewJobsWithImageRefresh(base *Jobs, repository ImageRefreshJobRepository, admit bool) (*Jobs, error) {
	if base == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	value := *base
	value.imageRefreshJobs, value.imageRefreshAdmit = repository, admit
	return &value, nil
}

// SubmitImageRefresh queues an image refresh of one library.
func (j *Jobs) SubmitImageRefresh(ctx context.Context, actor domain.Actor, library, key, priority string) (domain.Job, bool, error) {
	if ctx == nil || j == nil || !validTarget(actor, library) || !validKey(key) || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, false, err
	}
	if j.imageRefreshJobs == nil || !j.imageRefreshAdmit {
		return domain.Job{}, false, domain.ErrImageUnavailable
	}
	return j.imageRefreshJobs.SubmitImageRefreshJob(ctx, actor, library, key, priority, j.policy)
}

// ImageRefreshState returns the progress of an image_refresh job; any other
// job kind is domain.ErrNotFound.
func (j *Jobs) ImageRefreshState(ctx context.Context, actor domain.Actor, job string) (domain.ImageRefreshJobState, error) {
	if ctx == nil || j == nil || !validTarget(actor, job) {
		return domain.ImageRefreshJobState{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.ImageRefreshJobState{}, err
	}
	if j.imageRefreshJobs == nil {
		return domain.ImageRefreshJobState{}, domain.ErrImageUnavailable
	}
	return j.imageRefreshJobs.GetImageRefreshState(ctx, actor, job)
}
