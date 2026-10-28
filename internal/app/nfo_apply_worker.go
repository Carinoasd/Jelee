package app

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOApplyExecutionRepository is the durable side of an nfo_apply run. Every
// method is fenced by the job lease.
type NFOApplyExecutionRepository interface {
	// NextNFOApplyBatch returns up to limit item IDs after the job's cursor,
	// in ascending order, and the job's actor. It answers context.Canceled
	// once cancellation was requested and domain.ErrForbidden when the actor
	// is no longer an active administrator with a live session.
	NextNFOApplyBatch(ctx context.Context, lease domain.JobLease, limit int) (domain.NFOApplyBatch, error)
	// RecordNFOApplyBatch moves the cursor to cursor, adds counts and
	// refreshes the job's progress columns in one transaction.
	RecordNFOApplyBatch(ctx context.Context, lease domain.JobLease, cursor string, counts domain.NFOApplyCounts) error
	FinishNFOApply(ctx context.Context, lease domain.JobLease, state, code string) error
}

// NFOApplyWorker runs one nfo_apply job. Each item goes through
// Metadata.ApplyNFO, so field locks, manual values and every validation are
// the interactive path's; the worker only walks items and classifies results.
type NFOApplyWorker struct {
	metadata  *Metadata
	repo      NFOApplyExecutionRepository
	dbTimeout time.Duration
}

// NewNFOApplyWorker requires a Metadata that can apply NFO fields. dbTimeout
// bounds each repository call; zero uses two seconds.
func NewNFOApplyWorker(metadata *Metadata, repository NFOApplyExecutionRepository, dbTimeout time.Duration) (*NFOApplyWorker, error) {
	if metadata == nil || metadata.nfoFields == nil || repository == nil || dbTimeout < 0 {
		return nil, domain.ErrInvalid
	}
	if _, ok := metadata.items.(NFOItemApplyRepository); !ok {
		return nil, domain.ErrInvalid
	}
	if dbTimeout == 0 {
		dbTimeout = 2 * time.Second
	}
	return &NFOApplyWorker{metadata: metadata, repo: repository, dbTimeout: dbTimeout}, nil
}

func (w *NFOApplyWorker) db(ctx context.Context, call func(context.Context) error) error {
	c, cancel := context.WithTimeout(ctx, w.dbTimeout)
	defer cancel()
	return call(c)
}

// Run walks the library's items after the stored cursor until none remain.
// A returned error leaves the cursor at the last committed checkpoint. An
// item that fails locally never ends the run; storage, lease, authorization
// and context errors do.
func (w *NFOApplyWorker) Run(ctx context.Context, lease domain.JobLease) error {
	if ctx == nil || lease.Job.Kind != domain.JobNFOApply {
		return domain.ErrInvalid
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var batch domain.NFOApplyBatch
		if err := w.db(ctx, func(c context.Context) (err error) {
			batch, err = w.repo.NextNFOApplyBatch(c, lease, domain.NFOApplyBatchSize)
			return err
		}); err != nil {
			return err
		}
		if len(batch.Items) == 0 {
			return nil
		}
		var counts domain.NFOApplyCounts
		last := ""
		var stop error
		for _, item := range batch.Items {
			if stop = ctx.Err(); stop != nil {
				break
			}
			outcome, err := w.applyItem(ctx, batch.Actor, item)
			if err != nil {
				stop = err
				break
			}
			switch outcome {
			case nfoApplyApplied:
				counts.Applied++
			case nfoApplyUnchanged:
				counts.Unchanged++
			case nfoApplySkipped:
				counts.Skipped++
			default:
				counts.Failed++
			}
			last = item
		}
		if last != "" {
			// Finished items are recorded even when the run stops, so a
			// cancelled job keeps an exact count and a resume never repeats
			// them.
			recordCtx := context.WithoutCancel(ctx)
			if err := w.db(recordCtx, func(c context.Context) error {
				return w.repo.RecordNFOApplyBatch(c, lease, last, counts)
			}); err != nil {
				return err
			}
		}
		if stop != nil {
			return stop
		}
	}
}

type nfoApplyOutcome int

const (
	nfoApplyApplied nfoApplyOutcome = iota
	nfoApplyUnchanged
	nfoApplySkipped
	nfoApplyFailed
)

// applyItem reads the item's current metadata, which gives the revision and
// the state to compare with, and applies. A revision or source conflict is
// retried once with fresh state. The error is non-nil only when the run must
// stop.
func (w *NFOApplyWorker) applyItem(ctx context.Context, actor domain.Actor, item string) (nfoApplyOutcome, error) {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var before domain.ItemMetadata
		if before, err = w.metadata.ItemFields(ctx, actor, item); err != nil {
			break
		}
		var result domain.MetadataApplyResult
		result, err = w.metadata.ApplyNFO(ctx, actor, item, before.Revision, true)
		if err == nil {
			if nfoApplyChanged(before, result.Metadata) {
				return nfoApplyApplied, nil
			}
			return nfoApplyUnchanged, nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			break
		}
	}
	return classifyNFOApplyError(ctx, err)
}

// nfoApplyChanged reports whether applying changed what the item stores. The
// apply path lists every NFO field it wrote, also when the value was already
// there, so the stored values, sources and locks are compared instead.
func nfoApplyChanged(before, after domain.ItemMetadata) bool {
	if before.Kind != after.Kind || len(before.Fields) != len(after.Fields) || len(before.Facts) != len(after.Facts) {
		return true
	}
	for i, a := range after.Fields {
		b := before.Fields[i]
		if a.Field != b.Field || a.Value != b.Value || a.Source != b.Source || a.Locked != b.Locked || nfoOriginLocked(a.NFOOrigin, a.NFOLockOrigin) != nfoOriginLocked(b.NFOOrigin, b.NFOLockOrigin) {
			return true
		}
	}
	for i, a := range after.Facts {
		b := before.Facts[i]
		if a.Field != b.Field || !bytes.Equal(a.Value, b.Value) || a.Source != b.Source || a.Locked != b.Locked || nfoOriginLocked(a.NFOOrigin, a.NFOLockOrigin) != nfoOriginLocked(b.NFOOrigin, b.NFOLockOrigin) {
			return true
		}
	}
	return false
}

func nfoOriginLocked(origin *domain.NFOItemOrigin, lock *domain.NFOFieldLockOrigin) [2]bool {
	return [2]bool{origin != nil && origin.Locked, lock != nil && lock.Locked}
}

// classifyNFOApplyError maps an ApplyNFO error. Not applicable items (no or
// invalid NFO, no media source, unresolved scope, deleted item) are skipped,
// other item-local errors are failures, and anything that is not about this
// item stops the run.
func classifyNFOApplyError(ctx context.Context, err error) (nfoApplyOutcome, error) {
	switch {
	case ctx.Err() != nil:
		return nfoApplyFailed, ctx.Err()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, domain.ErrJobLeaseLost), errors.Is(err, domain.ErrDatabase),
		errors.Is(err, domain.ErrUnauthenticated), errors.Is(err, domain.ErrForbidden):
		return nfoApplyFailed, err
	case errors.Is(err, domain.ErrMetadataUnavailable), errors.Is(err, domain.ErrNotFound):
		return nfoApplySkipped, nil
	}
	return nfoApplyFailed, nil
}
