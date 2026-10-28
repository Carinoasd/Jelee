package jobs

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOExportOptions enables claiming nfo_export jobs (G39.14). The worker
// prepares one chunk of items, submits a child nfo_write job for it and gives
// the worker back; the repository does not offer the export job again until
// the child is terminal. Enable it only together with NFOWrite, which runs the
// children.
type NFOExportOptions struct {
	Worker     NFOExportStepper
	Repository app.NFOExportExecutionRepository
}

// NFOExportStepper advances one export job by a step; *app.NFOExportWorker is
// the production one.
type NFOExportStepper interface {
	Run(context.Context, domain.JobLease) error
}

var _ NFOExportStepper = (*app.NFOExportWorker)(nil)

// executeNFOExport runs one step of the export under the job context, so the
// monitor's heartbeat, cancellation and runtime limit stop it between items.
// A yield is not an error here; run hands the job back after it has joined the
// monitor.
func (r *Runner) executeNFOExport(ctx context.Context, lease domain.JobLease) (storage bool, err error) {
	options := r.options.NFOExport
	if options == nil {
		return false, domain.ErrScanUnavailable
	}
	err = options.Worker.Run(ctx, lease)
	switch {
	case err == nil, errors.Is(err, app.ErrNFOExportYield):
		return false, err
	case ctx.Err() != nil:
		return false, ctx.Err()
	case errors.Is(err, domain.ErrJobLeaseLost), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false, err
	}
	return true, err
}

// yieldNFOExport returns the job to the queue after a step that submitted or
// found a child job. A failure is left to lease expiry, which requeues the
// job the same way.
func (r *Runner) yieldNFOExport(ctx context.Context, lease domain.JobLease) {
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.options.DBOperationTimeout)
	defer cancel()
	if err := r.options.NFOExport.Repository.YieldNFOExport(dbCtx, lease); err != nil {
		r.logPersistenceFailure(ctx, lease)
	}
}
