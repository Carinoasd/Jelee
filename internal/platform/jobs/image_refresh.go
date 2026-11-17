package jobs

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ImageRefreshOptions enables claiming image_refresh jobs (G40.9, G40.10).
// The worker fetches one candidate at a time under the job lease and
// checkpoints every fetch, so a replacement lease resumes after the last
// recorded row. A worker without a fetcher fails every job it claims.
type ImageRefreshOptions struct {
	Worker     *app.ImageRefreshWorker
	Repository app.ImageRefreshExecutionRepository
}

// executeImageRefresh runs the worker under the job context, so the
// monitor's heartbeat, cancellation and runtime limit stop it between and
// during fetches.
func (r *Runner) executeImageRefresh(ctx context.Context, lease domain.JobLease) (storage bool, err error) {
	options := r.options.ImageRefresh
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
