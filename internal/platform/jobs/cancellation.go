package jobs

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.JobCancellationNotifier = (*Runner)(nil)

type runningCancellation struct {
	generation int64
	cancel     context.CancelCauseFunc
}

// The registry contains at most one entry per executing worker. It is not a
// scheduler or a substitute for committed cancellation flags and lease fences.
func (r *Runner) registerCancellation(l domain.JobLease, cancel context.CancelCauseFunc) func() {
	r.cancellationMu.Lock()
	if r.runningCancellations == nil {
		r.runningCancellations = make(map[string]runningCancellation)
	}
	r.runningCancellations[l.Job.ID] = runningCancellation{generation: l.Generation, cancel: cancel}
	r.cancellationMu.Unlock()
	return func() {
		r.cancellationMu.Lock()
		defer r.cancellationMu.Unlock()
		if current, ok := r.runningCancellations[l.Job.ID]; ok && current.generation == l.Generation {
			delete(r.runningCancellations, l.Job.ID)
		}
	}
}

// Called only after an authorized cancellation transaction has committed.
// Unknown IDs and owners on other instances use the durable heartbeat path.
func (r *Runner) NotifyJobCancellation(id string) {
	if r == nil || !domain.ValidID(id) {
		return
	}
	r.cancellationMu.Lock()
	current, found := r.runningCancellations[id]
	r.cancellationMu.Unlock()
	if found {
		current.cancel(errCancelRequested)
	}
}
