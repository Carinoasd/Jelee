package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOExportJobRepository admits and reports nfo_export jobs (G39.14).
type NFOExportJobRepository interface {
	SubmitNFOExportModeJob(ctx context.Context, actor domain.Actor, library, key, priority, mode string, policy domain.JobPolicy) (domain.Job, bool, error)
	GetNFOExportState(ctx context.Context, actor domain.Actor, job string) (domain.NFOExportJobState, error)
}

// NewJobsWithNFOExport lets base admit nfo_export jobs through repository. Only
// a runner that claims nfo_write and nfo_export jobs may be given one.
func NewJobsWithNFOExport(base *Jobs, repository NFOExportJobRepository) (*Jobs, error) {
	if base == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	value := *base
	value.nfoExportJobs = repository
	return &value, nil
}

// SubmitNFOExport queues a batch NFO export of one library. The library must
// be read-only with writes enabled; otherwise the repository answers
// domain.ErrNFOWriteDisabled. Without an export repository every library
// counts as not writable.
func (j *Jobs) SubmitNFOExport(ctx context.Context, actor domain.Actor, library, key, priority string) (domain.Job, bool, error) {
	return j.SubmitNFOExportMode(ctx, actor, library, key, priority, domain.NFOExportAll)
}

// SubmitNFOExportMode is SubmitNFOExport with an export mode: all writes the
// stored values into existing NFOs, missing creates the NFOs that do not exist.
// The mode is part of the request an idempotency key names.
func (j *Jobs) SubmitNFOExportMode(ctx context.Context, actor domain.Actor, library, key, priority, mode string) (domain.Job, bool, error) {
	if ctx == nil || j == nil || !validTarget(actor, library) || !validKey(key) || !domain.ValidNFOExportMode(mode) || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, false, err
	}
	if j.nfoExportJobs == nil {
		return domain.Job{}, false, domain.ErrNFOWriteDisabled
	}
	return j.nfoExportJobs.SubmitNFOExportModeJob(ctx, actor, library, key, priority, mode, j.policy)
}

// NFOExportState returns the progress of an nfo_export job; any other job kind
// is domain.ErrNotFound.
func (j *Jobs) NFOExportState(ctx context.Context, actor domain.Actor, job string) (domain.NFOExportJobState, error) {
	if ctx == nil || j == nil || !validTarget(actor, job) {
		return domain.NFOExportJobState{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOExportJobState{}, err
	}
	if j.nfoExportJobs == nil {
		return domain.NFOExportJobState{}, domain.ErrNFOReaderUnavailable
	}
	return j.nfoExportJobs.GetNFOExportState(ctx, actor, job)
}
