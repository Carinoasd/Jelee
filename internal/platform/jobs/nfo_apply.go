package jobs

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOApplyOptions enables claiming nfo_apply jobs (G39.14). The worker walks
// the library in checkpointed batches under the job lease, so a replacement
// lease resumes after the last committed batch.
type NFOApplyOptions struct {
	Worker     *app.NFOApplyWorker
	Repository app.NFOApplyExecutionRepository
}

// executeNFOApply runs the worker under the job context, so the monitor's
// heartbeat, cancellation and runtime limit stop it between items.
func (r *Runner) executeNFOApply(ctx context.Context, lease domain.JobLease) (storage bool, err error) {
	options := r.options.NFOApply
	if options == nil {
		return false, domain.ErrScanUnavailable
	}
	err = options.Worker.Run(ctx, lease)
	switch {
	case err == nil:
		return false, nil
	case ctx.Err() != nil:
		return false, ctx.Err()
	case errors.Is(err, domain.ErrJobLeaseLost), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false, err
	}
	return true, err
}
