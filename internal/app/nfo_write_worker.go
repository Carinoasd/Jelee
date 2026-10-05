package app

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOWriteCommitter is the filesystem side of one journaled entry. The NFO
// adapter re-observes the job-owned target and rechecks every binding itself.
type NFOWriteCommitter interface {
	// CommitNFOWriteEntry stages and settles the token; nil means replaced.
	// Errors that retrying cannot fix wrap domain.ErrNFOWriteRejected.
	CommitNFOWriteEntry(context.Context, domain.JobLease, domain.NFOWriteCommitRecord, NFOWriteCommitSettlementRepository) error
	// AbortNFOWriteEntry concludes the token without any forward Rename.
	AbortNFOWriteEntry(context.Context, domain.JobLease, domain.NFOWriteCommitRecord, NFOWriteCommitSettlementRepository) (domain.NFOWriteCommitSettlementPhase, error)
}

// ErrNFOWritePartial reports a finished run in which at least one entry was
// rejected and concluded without replacement. The job fails as a whole.
var ErrNFOWritePartial = errors.New("nfo write entries rejected")

// NFOWriteWorker runs claimed nfo_write jobs and recovers stopped ones. It
// holds no state between calls; PostgreSQL fences every step.
type NFOWriteWorker struct {
	repository    NFOWriteExecutionRepository
	committer     NFOWriteCommitter
	owner         string
	recoveryLease time.Duration
}

func NewNFOWriteWorker(repository NFOWriteExecutionRepository, committer NFOWriteCommitter, owner string, recoveryLease time.Duration) (*NFOWriteWorker, error) {
	if repository == nil || committer == nil || !domain.ValidID(owner) || recoveryLease < 3*time.Second || recoveryLease > time.Hour {
		return nil, domain.ErrInvalid
	}
	return &NFOWriteWorker{repository: repository, committer: committer, owner: owner, recoveryLease: recoveryLease}, nil
}

// Run writes every entry of a claimed job in sequence order. A rejected entry
// is concluded without replacement and the run continues, so one changed file
// does not block the rest of a batch; the run then ends with ErrNFOWritePartial.
// Any other error stops the run: the caller stops the job and the recovery
// lease resumes the journaled tokens. Cancellation surfaces as a context error
// from ctx; the caller must not report success after it.
func (w *NFOWriteWorker) Run(ctx context.Context, lease domain.JobLease) error {
	if w == nil || ctx == nil || lease.Job.Kind != domain.JobNFOWrite || lease.RecoveryEpoch != 0 {
		return domain.ErrInvalid
	}
	entries, err := w.repository.ListNFOWriteCommitEntries(ctx, lease)
	if err != nil {
		return err
	}
	rejected := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch entry.Settlement {
		case domain.NFOWriteCommitReplaced:
			continue
		case domain.NFOWriteCommitRolledBack:
			rejected++
			continue
		}
		record, err := w.repository.BeginNFOWriteCommit(ctx, lease, entry.Sequence)
		if errors.Is(err, domain.ErrConflict) && entry.Token == "" {
			// The catalog scope changed or another job still claims the target;
			// no journal was opened, so nothing is retained for this entry.
			rejected++
			continue
		}
		if err != nil {
			return err
		}
		err = w.committer.CommitNFOWriteEntry(ctx, lease, record, w.repository)
		if err == nil {
			continue
		}
		if !errors.Is(err, domain.ErrNFOWriteRejected) {
			return err
		}
		rejected++
		if _, err := w.committer.AbortNFOWriteEntry(ctx, lease, record, w.repository); err != nil {
			return err
		}
	}
	if rejected > 0 {
		return ErrNFOWritePartial
	}
	return nil
}

// RecoverPending resolves up to limit stopped, unresolved jobs. A job whose
// recovery lease is held elsewhere is skipped. It returns the resolved count
// and the first error, after attempting every listed job.
func (w *NFOWriteWorker) RecoverPending(ctx context.Context, limit int) (int, error) {
	if w == nil || ctx == nil {
		return 0, domain.ErrInvalid
	}
	ids, err := w.repository.ListNFOWriteCommitRecoveries(ctx, limit)
	if err != nil {
		return 0, err
	}
	resolved := 0
	var first error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return resolved, err
		}
		_, err := w.Recover(ctx, id)
		if errors.Is(err, domain.ErrConflict) {
			continue
		}
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		resolved++
	}
	return resolved, first
}

// Recover takes the recovery lease of one stopped job, renews it while working
// and continues only its existing tokens. An uncancelled job finishes each
// started write; a token that cannot finish, and every token of a cancelled
// job, is concluded without replacement. Context, lease and database errors
// keep the job unresolved for a later pass. On success the job is resolved and
// its final state is returned.
func (w *NFOWriteWorker) Recover(ctx context.Context, jobID string) (string, error) {
	if w == nil || ctx == nil || !domain.ValidID(jobID) {
		return "", domain.ErrInvalid
	}
	lease, err := w.repository.AcquireNFOWriteCommitRecovery(ctx, jobID, w.owner, w.recoveryLease)
	if err != nil {
		return "", err
	}
	work, cancel := context.WithCancelCause(ctx)
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		for {
			timer := time.NewTimer(w.recoveryLease / 3)
			select {
			case <-work.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if _, err := w.repository.RenewNFOWriteCommitRecovery(work, lease, w.recoveryLease); err != nil {
				cancel(err)
				return
			}
		}
	}()
	defer func() {
		cancel(nil)
		<-renewed
	}()
	state, err := w.recover(work, lease)
	if err != nil {
		if cause := context.Cause(work); cause != nil && ctx.Err() == nil && !errors.Is(cause, context.Canceled) {
			return "", cause
		}
		return "", err
	}
	return state, nil
}

func (w *NFOWriteWorker) recover(ctx context.Context, lease domain.JobLease) (string, error) {
	rollbackOnly := lease.Job.State == domain.JobCancelled || lease.Job.CancelRequested
	entries, err := w.repository.ListNFOWriteCommitEntries(ctx, lease)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if entry.Token == "" || entry.Settlement.Terminal() {
			continue
		}
		// The evidence read applies no catalog fence, so a cancelled or drifted
		// entry can still be rolled back.
		evidence, err := w.repository.GetNFOWriteCommitFiles(ctx, lease, entry.Sequence, entry.Token)
		if err != nil {
			return "", err
		}
		if !rollbackOnly {
			err = w.committer.CommitNFOWriteEntry(ctx, lease, evidence.Record, w.repository)
			if err == nil {
				continue
			}
			if nfoWriteRetryable(ctx, err) {
				return "", err
			}
		}
		if _, err := w.committer.AbortNFOWriteEntry(ctx, lease, evidence.Record, w.repository); err != nil {
			return "", err
		}
	}
	return w.repository.CompleteNFOWriteCommitRecovery(ctx, lease)
}

// nfoWriteRetryable separates lost leases and storage outages, which a later
// recovery pass retries, from failures that conclude the token.
func nfoWriteRetryable(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, domain.ErrJobLeaseLost) || errors.Is(err, domain.ErrDatabase)
}
