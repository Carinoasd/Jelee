package jobs

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ConsistencyOptions enables claiming consistency_check jobs (G50.3). The
// checker reads in bounded pages and only reports; a job run never repairs.
// Template carries the bounds of every run; origin, job and library come
// from the lease.
type ConsistencyOptions struct {
	Checker    *app.ConsistencyChecker
	Repository app.ConsistencyExecutionRepository
	Template   app.ConsistencyOptions
}

// executeConsistency runs the checker under the job context, so the
// monitor's heartbeat, cancellation and runtime limit stop it between pages.
// The checker persists its report itself, also when it is cancelled.
func (r *Runner) executeConsistency(ctx context.Context, lease domain.JobLease) (storage bool, err error) {
	options := r.options.Consistency
	if options == nil {
		return false, domain.ErrScanUnavailable
	}
	run := options.Template
	run.Origin, run.JobID, run.Library, run.Fix = domain.ConsistencyOriginJob, lease.Job.ID, lease.Job.LibraryID, false
	if _, err = options.Checker.Run(ctx, run); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		if errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrNotFound) {
			return false, err
		}
		return true, err
	}
	return false, nil
}
