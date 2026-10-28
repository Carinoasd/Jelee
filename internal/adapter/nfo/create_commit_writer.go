package nfo

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// A create entry has no source to hold: the job-owned preparation carries the
// recipe and the receipt of the absent target. CommitNFOWriteEntry routes it
// here after reading the durable state; the rewrite protocol is not involved.

// commitCreateEntry stages the output (unless settlement began) and settles the
// token by linking the output to the absent target. nil means created.
func (w *Writer) commitCreateEntry(ctx context.Context, lease domain.JobLease, record domain.NFOWriteCommitRecord, settlement domain.NFOWriteCommitSettlement, repository app.NFOWriteCommitSettlementRepository) error {
	ops := nativeNFOWriteOperations()
	// After a durable phase 1 the output may already be the target; Stage would
	// refuse it. Without phase 1 the token never linked, so Stage still applies.
	if settlement.Phase == 0 {
		if err := w.stageCreateFiles(ctx, lease, record, repository, ops); err != nil {
			return err
		}
	}
	return w.settleCreateFiles(ctx, lease, record, repository, ops)
}

func nfoCreateRecordValid(lease domain.JobLease, record domain.NFOWriteCommitRecord) bool {
	return domain.ValidID(record.Token) && record.JobID == lease.Job.ID && (record.Owner == lease.Owner || lease.RecoveryEpoch != 0) && record.Generation == lease.Generation && record.Sequence >= 1 && record.Sequence <= 100
}

// openNFOCreateDirectory opens the parent directory of the NFO by walking the
// scope path without following symlinks. The caller closes both handles.
func openNFOCreateDirectory(scope domain.NFOItemScope) (root, directory *os.Root, err error) {
	rootPath := filepath.Clean(scope.Source.RootPath)
	if info, err := os.Lstat(rootPath); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, ErrChanged
	}
	root, err = os.OpenRoot(rootPath + string(os.PathSeparator) + ".")
	if err != nil {
		return nil, nil, ErrChanged
	}
	parts := strings.Split(scope.Source.RelativePath, "/")
	for i := range parts[:len(parts)-1] {
		info, err := root.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, nil, ErrChanged
		}
	}
	directory, err = root.OpenRoot(filepath.Dir(filepath.FromSlash(scope.Source.RelativePath)))
	if err != nil {
		_ = root.Close()
		return nil, nil, ErrChanged
	}
	return root, directory, nil
}

// boundCreateCheck rechecks the persisted native scope of a create: the
// configured root, every ancestor and the media are the first observed objects
// and the NFO name is still absent, and the held directory is the observed
// parent. It is an observation, never filesystem authorization. It is meant
// for the steps before the link; after it the target is the output object.
func boundCreateCheck(directory *os.Root, scope domain.NFOItemScope, receipt domain.NFONativePreparationReceipt) func(context.Context) error {
	return func(checkCtx context.Context) error {
		if err := checkCtx.Err(); err != nil {
			return err
		}
		if !receipt.TargetAbsent() {
			return ErrInvalidInput
		}
		if err := verifyNativePreparationScope(checkCtx, scope, receipt); err != nil {
			return err
		}
		ancestors := receipt.AncestorIdentities()
		current, err := nativeIdentityWithin(directory, ".")
		if err != nil || len(ancestors) == 0 || current.record != ancestors[len(ancestors)-1] {
			return ErrChanged
		}
		return nil
	}
}

func (w *Writer) stageCreateFiles(ctx context.Context, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository, ops nfoWriteOperations) error {
	if w == nil || w.budget == nil || ctx == nil || repository == nil || !nfoCreateRecordValid(lease, record) {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, ok := w.budget.(app.PayloadBudget)
	if !ok {
		return ErrInvalidInput
	}
	// SQL bounds each payload by NFOMaxSourceBytes; reserve the worst case.
	releasePayload, err := payload.ReservePayloadBytes(ctx, 3*domain.NFOMaxSourceBytes)
	if err != nil {
		return err
	}
	defer releasePayload()
	releaseRead, err := w.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return err
	}
	task, err := repository.GetNFOWriteTask(ctx, lease, record.Sequence)
	var evidence domain.NFOWriteCommitFileEvidence
	if err == nil {
		evidence, err = repository.GetNFOWriteCommitFiles(ctx, lease, record.Sequence, record.Token)
	}
	releaseRead()
	if err != nil {
		return err
	}
	prepared := task.Preparation
	if task.JobID != record.JobID || task.Sequence != record.Sequence || prepared.Scope.RootGeneration < 1 || !prepared.Request.Create || !prepared.NativeObservation.TargetAbsent() || domain.ValidateNFOWritePreparation(prepared) != nil {
		return ErrInvalidInput
	}
	if evidence.Record.JobID != record.JobID || evidence.Record.Sequence != record.Sequence || evidence.Record.Owner != record.Owner || evidence.Record.Generation != record.Generation || evidence.Record.Token != record.Token || evidence.ReadyRecorded && !evidence.PlanRecorded || evidence.PlanRecorded && !evidence.Plan.Create() {
		return ErrChanged
	}
	if evidence.CheckpointRecorded {
		checkpoint := evidence.Checkpoint
		if !evidence.PlanRecorded || checkpoint.Phase != 1 || domain.ValidateNFOWriteCommitFileCheckpoint(checkpoint) != nil || checkpoint.OutputIdentity[1] != evidence.Plan.ParentIdentity[1] || evidence.ReadyRecorded && checkpoint.OutputIdentity != evidence.Ready.OutputIdentity {
			return ErrChanged
		}
	}
	replacement, err := w.rebuildPreparedCreate(ctx, prepared)
	if err != nil {
		return err
	}
	releaseIO, err := w.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return err
	}
	defer releaseIO()
	root, directory, err := openNFOCreateDirectory(prepared.Scope)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	defer func() { _ = directory.Close() }()
	filename := path.Base(prepared.Scope.Source.RelativePath)
	check := boundCreateCheck(directory, prepared.Scope, prepared.NativeObservation)
	if err := check(ctx); err != nil {
		return err
	}
	var token [16]byte
	decoded, err := hex.DecodeString(strings.ReplaceAll(record.Token, "-", ""))
	if err != nil || len(decoded) != len(token) {
		return ErrInvalidInput
	}
	copy(token[:], decoded)
	ops.documentsValidated, ops.checkSource = true, check
	// A nil original selects the create staging in the shared orchestration.
	return stageNFOCommitAttempts(ctx, directory, filename, nil, replacement, token, lease, record, evidence, repository, check, ops)
}

func (w *Writer) settleCreateFiles(ctx context.Context, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository, ops nfoWriteOperations) error {
	if w == nil || w.budget == nil || ctx == nil || repository == nil || !nfoCreateRecordValid(lease, record) {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if lease.Job.CancelRequested {
		phase, err := w.abortCommitFiles(ctx, lease, record, repository, ops)
		if err != nil {
			return err
		}
		switch phase {
		case domain.NFOWriteCommitReplaced:
			return nil
		case domain.NFOWriteCommitRolledBack:
			return ErrRolledBack
		}
		return ErrCommitNotReady
	}
	payload, ok := w.budget.(app.PayloadBudget)
	if !ok {
		return ErrInvalidInput
	}
	releasePayload, err := payload.ReservePayloadBytes(ctx, 3*domain.NFOMaxSourceBytes)
	if err != nil {
		return err
	}
	defer releasePayload()
	input, err := w.readNFOCommitSettlementInput(ctx, lease, record, repository)
	if err != nil {
		return err
	}
	switch input.settlement.Phase {
	case domain.NFOWriteCommitReplaced:
		return nil
	case domain.NFOWriteCommitRolledBack:
		return ErrRolledBack
	}
	prepared := input.task.Preparation
	if !prepared.Request.Create || !prepared.NativeObservation.TargetAbsent() || !input.files.plan.create() {
		return ErrInvalidInput
	}
	releaseIO, err := w.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return err
	}
	defer releaseIO()
	root, directory, err := openNFOCreateDirectory(prepared.Scope)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	defer func() { _ = directory.Close() }()
	ops.checkSource = boundCreateCheck(directory, prepared.Scope, prepared.NativeObservation)
	progress := func(ctx context.Context, phase nfoSettlePhase) error {
		value := domain.NFOWriteCommitBackedUp
		if phase == nfoSettleReplaced {
			value = domain.NFOWriteCommitReplaced
		} else if phase != nfoSettleBackedUp {
			return ErrInvalidInput
		}
		saved, err := repository.SaveNFOWriteCommitSettlement(ctx, lease, record.Sequence, record.Token, input.attempt, value)
		if err != nil {
			return err
		}
		if saved.Attempt != input.attempt || saved.Phase != value {
			return ErrChanged
		}
		return nil
	}
	phase, err := settleNFOCreateFiles(ctx, directory, input.files, prepared.Replacement, input.settlement.Phase == domain.NFOWriteCommitBackedUp, progress, ops)
	switch {
	case err == nil && phase == nfoSettleReplaced:
		return nil
	case phase == nfoSettleRolledBack && errors.Is(err, ErrRolledBack), errors.Is(err, ErrTargetAppeared):
		// Either the link was undone or the name belongs to someone else. In
		// both cases the token ends without a target, and the object found at
		// the name was not touched.
		if _, saveErr := w.saveNFOCommitRollback(ctx, lease, record, repository, input); saveErr != nil {
			return saveErr
		}
		return err
	case err == nil:
		return ErrReplace
	}
	return err
}
