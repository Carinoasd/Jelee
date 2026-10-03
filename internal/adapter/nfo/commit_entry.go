package nfo

import (
	"context"
	"errors"
	"fmt"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.NFOWriteCommitter = (*Writer)(nil)

// CommitNFOWriteEntry observes the job-owned target afresh, finishes Stage
// unless settlement already began, and settles the token. nil means replaced.
// Errors that retrying cannot fix wrap domain.ErrNFOWriteRejected; any other
// error leaves a resumable state for the same lease or the recovery lease.
func (w *Writer) CommitNFOWriteEntry(ctx context.Context, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository) error {
	if w == nil || ctx == nil || repository == nil {
		return ErrInvalidInput
	}
	task, err := repository.GetNFOWriteTask(ctx, lease, record.Sequence)
	if err != nil {
		return classifyNFOWriteEntry(err)
	}
	settlement, err := repository.GetNFOWriteCommitSettlement(ctx, lease, record.Sequence, record.Token)
	if err != nil {
		return classifyNFOWriteEntry(err)
	}
	switch settlement.Phase {
	case domain.NFOWriteCommitReplaced:
		return nil
	case domain.NFOWriteCommitRolledBack:
		return classifyNFOWriteEntry(ErrRolledBack)
	}
	scope := task.Preparation.Scope
	source, err := ReadSource(ctx, scope.Source.RootPath, scope.Source.RelativePath, task.Preparation.Request.MaxBytes)
	if err != nil {
		return classifyNFOWriteEntry(err)
	}
	// After a durable backup phase the target may already be renamed, so the
	// source no longer matches the prepared original; Stage would refuse it.
	if settlement.Phase == 0 {
		if err := w.StageCommitFiles(ctx, source, lease, record, repository); err != nil {
			return classifyNFOWriteEntry(err)
		}
	}
	return classifyNFOWriteEntry(w.SettleCommitFiles(ctx, source, lease, record, repository))
}

// AbortNFOWriteEntry concludes the token without replacement; see
// AbortCommitFiles. A token that never reached ready returns phase 0.
func (w *Writer) AbortNFOWriteEntry(ctx context.Context, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository) (domain.NFOWriteCommitSettlementPhase, error) {
	return w.AbortCommitFiles(ctx, lease, record, repository)
}

func classifyNFOWriteEntry(err error) error {
	if err == nil || errors.Is(err, domain.ErrNFOWriteRejected) {
		return err
	}
	for _, rejected := range []error{ErrChanged, ErrCommitAttemptsExhausted, ErrRolledBack, ErrInvalidInput, ErrNotFound, ErrTooLarge, ErrTooComplex, ErrInvalidXML, ErrUnsafeXML, ErrInvalidEncoding, ErrUnsupportedEncoding, ErrEditAmbiguous, ErrEditLocked, ErrEditMissing, errNativeIdentity, domain.ErrConflict} {
		if errors.Is(err, rejected) {
			return fmt.Errorf("%w: %w", domain.ErrNFOWriteRejected, err)
		}
	}
	return err
}
