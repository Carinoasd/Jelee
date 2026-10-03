package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOWriteOptions enables nfo_write claims and the recovery loop. Without it
// the runner never claims an nfo_write job and never acquires recovery leases.
type NFOWriteOptions struct {
	Repository app.NFOWriteExecutionRepository
	Committer  app.NFOWriteCommitter
	// RecoveryInterval paces the scan for stopped, unresolved jobs.
	RecoveryInterval time.Duration
	// RecoveryLease is the TTL of each recovery lease; it is renewed at a third.
	RecoveryLease time.Duration
	// RecoveryBatch bounds how many jobs one pass resolves.
	RecoveryBatch int
	worker        *app.NFOWriteWorker
}

func (r *Runner) configureNFOWrite() error {
	if r.options.NFOWrite == nil {
		return nil
	}
	n := *r.options.NFOWrite
	if n.RecoveryInterval == 0 {
		n.RecoveryInterval = 5 * time.Second
	}
	if n.RecoveryLease == 0 {
		n.RecoveryLease = r.options.LeaseDuration
	}
	if n.RecoveryBatch == 0 {
		n.RecoveryBatch = 4
	}
	if _, ok := r.repository.(stagesClaimer); !ok || n.Repository == nil || n.Committer == nil || n.RecoveryInterval < 100*time.Millisecond || n.RecoveryInterval > 10*time.Minute || n.RecoveryBatch < 1 || n.RecoveryBatch > 100 {
		return domain.ErrInvalid
	}
	worker, err := app.NewNFOWriteWorker(n.Repository, n.Committer, r.options.Owner, n.RecoveryLease)
	if err != nil {
		return err
	}
	n.worker = worker
	r.options.NFOWrite = &n
	return nil
}

// executeNFOWrite runs the entries under the claimed lease. A rejected entry
// fails the job without being a repository error.
func (r *Runner) executeNFOWrite(ctx context.Context, lease domain.JobLease) (error, bool) {
	options := r.options.NFOWrite
	if options == nil {
		return domain.ErrScanUnavailable, false
	}
	err := options.worker.Run(ctx, lease)
	if err == nil || errors.Is(err, app.ErrNFOWritePartial) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err, false
	}
	return err, true
}

// finishNFOWrite stops the job. An unresolved stop is left to the recovery
// loop, which may still need to roll back a token between backup and Rename.
func (r *Runner) finishNFOWrite(ctx context.Context, lease domain.JobLease, state, code string) error {
	options := r.options.NFOWrite
	if options == nil {
		return domain.ErrInvalid
	}
	resolved, err := options.Repository.FinishNFOWriteJob(ctx, lease, state, code)
	if err == nil && !resolved {
		r.logger.Info("nfo write job awaits recovery", "component", "jobs", "taskId", lease.Job.ID, "state", state)
	}
	return err
}

// recoverNFOWrites is a service-lifetime loop. Recovery restores safety for
// stopped writes, so it ignores the work window.
func (r *Runner) recoverNFOWrites(ctx context.Context) {
	options := r.options.NFOWrite
	for r.wait(ctx, options.RecoveryInterval) {
		resolved, err := options.worker.RecoverPending(ctx, options.RecoveryBatch)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.logger.Warn("nfo write recovery incomplete", "component", "jobs", "code", "nfo_write_recovery")
		}
		if resolved > 0 {
			r.logger.Info("nfo write jobs recovered", "component", "jobs", "count", resolved)
		}
	}
}
