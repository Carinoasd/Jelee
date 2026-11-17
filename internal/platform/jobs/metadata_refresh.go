package jobs

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// MetadataRefreshOptions enables claiming metadata_refresh jobs (G13.6,
// G14.4). The worker matches and applies one item at a time under the job
// lease and checkpoints every item, so a replacement lease resumes after the
// last recorded item. A worker without a provider fails every job it claims.
type MetadataRefreshOptions struct {
	Worker     *app.MetadataRefreshWorker
	Repository app.MetadataRefreshExecutionRepository
}

// executeMetadataRefresh runs the worker under the job context, so the
// monitor's heartbeat, cancellation and runtime limit stop it between and
// during items.
func (r *Runner) executeMetadataRefresh(ctx context.Context, lease domain.JobLease) (storage bool, err error) {
	options := r.options.MetadataRefresh
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
