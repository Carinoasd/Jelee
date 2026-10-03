package nfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ErrCommitNotReady reports a token without complete ready evidence. Nothing
// was renamed for it; Stage must finish first, or the token is abandoned.
var ErrCommitNotReady = errors.New("nfo_commit_not_ready")

// SettleCommitFiles replaces the target with the ready files of a staged token.
// It repeats the Stage rechecks (job-owned intent, source/root/parent binding,
// native scope and lock), saves the backed-up phase before the target Rename
// and the replaced phase after it, and resumes from durable phases and the
// observed target. nil means replaced; ErrRolledBack means the target was
// restored and recorded as rolled back. Any other error leaves a resumable
// state. A lease on a cancelled job never replaces: it aborts instead.
func (w *Writer) SettleCommitFiles(ctx context.Context, source *Source, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository) error {
	return w.settleCommitFiles(ctx, source, lease, record, repository, nativeNFOWriteOperations())
}

func (w *Writer) settleCommitFiles(ctx context.Context, source *Source, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository, ops nfoWriteOperations) error {
	if w == nil || w.budget == nil || ctx == nil || source == nil || !source.ready || source.rootInfo == nil || source.parentInfo == nil || source.fileInfo == nil || repository == nil || !domain.ValidID(record.Token) || record.JobID != lease.Job.ID || record.Owner != lease.Owner && lease.RecoveryEpoch == 0 || record.Generation != lease.Generation || record.Sequence < 1 || record.Sequence > 100 {
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
	value := reflect.ValueOf(repository)
	var repositoryID uint64
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return ErrInvalidInput
		}
		repositoryID = uint64(value.Pointer())
	} else {
		w.mu.Lock()
		w.sequence++
		repositoryID = w.sequence
		w.mu.Unlock()
	}
	if len(source.rootPath) > 32768 || len(source.relative) > domain.ScanPathMaxBytes || len(lease.Owner) > 1024 {
		return ErrInvalidInput
	}
	encoded, err := json.Marshal(struct {
		RepositoryType      string
		RepositoryID        uint64
		JobID, Owner, Token string
		Generation          int64
		RecoveryEpoch       int64
		Sequence            int
		Root, Relative      string
		Stamp               SourceStamp
		MaxBytes            int64
	}{value.Type().String(), repositoryID, lease.Job.ID, lease.Owner, record.Token, lease.Generation, lease.RecoveryEpoch, record.Sequence, source.rootPath, source.relative, source.stamp, source.maxBytes})
	if err != nil {
		return ErrInvalidInput
	}
	digest := sha256.Sum256(encoded)
	return w.runIntent(ctx, "settle:"+hex.EncodeToString(digest[:]), source, ops, func() error {
		return w.settleCommitFilesOwned(ctx, source, lease, record, repository, ops)
	})
}

// nfoCommitSettlementInput is the durable state read before a settlement.
type nfoCommitSettlementInput struct {
	task       domain.NFOWriteTask
	files      nfoCommitFiles
	attempt    uint8
	settlement domain.NFOWriteCommitSettlement
}

// readNFOCommitSettlementInput reads the job-owned intent and the selected
// ready namespace. Legacy ready and an attempt ready are mutually exclusive.
func (w *Writer) readNFOCommitSettlementInput(ctx context.Context, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository) (nfoCommitSettlementInput, error) {
	var input nfoCommitSettlementInput
	releaseRead, err := w.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return input, err
	}
	task, err := repository.GetNFOWriteTask(ctx, lease, record.Sequence)
	var evidence domain.NFOWriteCommitFileEvidence
	if err == nil {
		evidence, err = repository.GetNFOWriteCommitFiles(ctx, lease, record.Sequence, record.Token)
	}
	// Neither read applies the catalog fence: a rollback must stay possible.
	if err == nil {
		input.settlement, err = repository.GetNFOWriteCommitSettlement(ctx, lease, record.Sequence, record.Token)
	}
	releaseRead()
	if err != nil {
		return input, err
	}
	if task.JobID != record.JobID || task.Sequence != record.Sequence || task.Preparation.Scope.RootGeneration < 1 || task.Preparation.NativeObservation.Empty() || domain.ValidateNFOWritePreparation(task.Preparation) != nil {
		return input, ErrInvalidInput
	}
	if evidence.Record.JobID != record.JobID || evidence.Record.Sequence != record.Sequence || evidence.Record.Owner != record.Owner || evidence.Record.Generation != record.Generation || evidence.Record.Token != record.Token {
		return input, ErrChanged
	}
	input.task = task
	var token [16]byte
	decoded, err := hex.DecodeString(strings.ReplaceAll(record.Token, "-", ""))
	if err != nil || len(decoded) != len(token) {
		return input, ErrInvalidInput
	}
	copy(token[:], decoded)
	if !evidence.PlanRecorded {
		if evidence.ReadyRecorded || input.settlement.Phase != 0 {
			return input, ErrChanged
		}
		return input, ErrCommitNotReady
	}
	plan := nfoCommitFilePlan{version: evidence.Plan.Version, token: token, filename: evidence.Plan.TargetName, parent: nfoNativeIdentity{evidence.Plan.ParentIdentity}, target: nfoNativeIdentity{evidence.Plan.TargetIdentity}}
	selected := input.settlement
	if !selected.ReadyRecorded {
		if evidence.ReadyRecorded || selected.Phase != 0 {
			return input, ErrChanged
		}
		return input, ErrCommitNotReady
	}
	// Legacy ready (attempt 0) and an attempt ready are mutually exclusive.
	if evidence.ReadyRecorded != (selected.ReadyAttempt == 0) || evidence.ReadyRecorded && evidence.Ready != selected.Ready || domain.ValidateNFOWriteCommitFilesReady(selected.Ready) != nil {
		return input, ErrChanged
	}
	plan.attempt = selected.ReadyAttempt
	input.attempt = selected.ReadyAttempt
	input.files = nfoCommitFiles{plan: plan, output: nfoNativeIdentity{selected.Ready.OutputIdentity}, rollback: nfoNativeIdentity{selected.Ready.RollbackIdentity}}
	if selected.Phase != 0 && selected.Attempt != input.attempt {
		return input, ErrChanged
	}
	if input.files.plan.filename != filepath.Base(filepath.FromSlash(task.Preparation.Scope.Source.RelativePath)) {
		return input, ErrChanged
	}
	return input, nil
}

func (w *Writer) settleCommitFilesOwned(ctx context.Context, source *Source, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository, ops nfoWriteOperations) error {
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
	if source.rootPath != filepath.Clean(prepared.Scope.Source.RootPath) || source.relative != prepared.Scope.Source.RelativePath || source.maxBytes != prepared.Request.MaxBytes {
		return ErrChanged
	}
	// Reconstruct before holding I/O; only the untouched target needs it.
	_, rebuildErr := w.rebuildPrepared(ctx, source, prepared)
	releaseIO, err := w.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return err
	}
	defer releaseIO()
	root, err := os.OpenRoot(source.rootPath + string(os.PathSeparator) + ".")
	if err != nil {
		return ErrChanged
	}
	defer root.Close()
	parent := filepath.Dir(filepath.FromSlash(source.relative))
	directory, err := root.OpenRoot(parent)
	if err != nil {
		return ErrChanged
	}
	defer directory.Close()
	filename := input.files.plan.filename
	current, err := nativeIdentityWithin(directory, filename)
	if err != nil {
		return ErrChanged
	}
	receipt := prepared.NativeObservation
	switch current {
	case input.files.plan.target:
		if rebuildErr != nil {
			return rebuildErr
		}
	case input.files.output, input.files.rollback:
		// Only this protocol renames after a durable backup phase. The held
		// source then observes the renamed object, so bind the scope to it.
		expected := prepared.Replacement
		if current == input.files.rollback {
			expected = prepared.Original
		}
		if input.settlement.Phase != domain.NFOWriteCommitBackedUp || !bytes.Equal(source.original, expected) {
			return ErrChanged
		}
		receipt, err = domain.NewNFONativePreparationReceipt(receipt.RootIdentity(), receipt.MediaIdentity(), current.record, receipt.AncestorIdentities())
		if err != nil {
			return ErrChanged
		}
	default:
		return ErrChanged
	}
	check := boundCommitCheck(source, root, directory, parent, filename, prepared.Scope, receipt)
	if err := check(ctx); err != nil {
		return err
	}
	ops.documentsValidated, ops.checkSource = true, check
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
	phase, err := settleNFOCommitFiles(ctx, directory, input.files, prepared.Original, prepared.Replacement, prepared.Request.Backups, progress, ops)
	switch {
	case err == nil && phase == nfoSettleReplaced:
		return nil
	case phase == nfoSettleRolledBack && errors.Is(err, ErrRolledBack):
		saved, saveErr := repository.SaveNFOWriteCommitSettlement(ctx, lease, record.Sequence, record.Token, input.attempt, domain.NFOWriteCommitRolledBack)
		if saveErr != nil {
			return saveErr
		}
		if saved.Phase != domain.NFOWriteCommitRolledBack {
			return ErrChanged
		}
		return ErrRolledBack
	case err == nil:
		return ErrReplace
	}
	return err
}

// AbortCommitFiles concludes a token without replacement, for cancellation or
// an unrecoverable settlement. It reads only durable evidence and the job-owned
// intent; it never renames forward. A token without ready evidence was never
// renamed and is left unsettled (phase 0, nil); a ready token without a backup
// phase is recorded as rolled back without touching files. After a backup
// phase the target is restored if it holds the output, and a foreign target is
// refused (ErrChanged). A terminal phase is returned as recorded, so an already
// replaced token stays replaced.
func (w *Writer) AbortCommitFiles(ctx context.Context, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository) (domain.NFOWriteCommitSettlementPhase, error) {
	if w == nil || w.budget == nil || ctx == nil || repository == nil || !domain.ValidID(record.Token) || record.JobID != lease.Job.ID || record.Owner != lease.Owner && lease.RecoveryEpoch == 0 || record.Generation != lease.Generation || record.Sequence < 1 || record.Sequence > 100 {
		return 0, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return w.abortCommitFiles(ctx, lease, record, repository, nativeNFOWriteOperations())
}

func (w *Writer) abortCommitFiles(ctx context.Context, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository, ops nfoWriteOperations) (domain.NFOWriteCommitSettlementPhase, error) {
	payload, ok := w.budget.(app.PayloadBudget)
	if !ok {
		return 0, ErrInvalidInput
	}
	releasePayload, err := payload.ReservePayloadBytes(ctx, 3*domain.NFOMaxSourceBytes)
	if err != nil {
		return 0, err
	}
	defer releasePayload()
	input, err := w.readNFOCommitSettlementInput(ctx, lease, record, repository)
	if errors.Is(err, ErrCommitNotReady) {
		return 0, nil
	}
	if err != nil {
		return input.settlement.Phase, err
	}
	if input.settlement.Phase.Terminal() {
		return input.settlement.Phase, nil
	}
	if input.settlement.Phase == 0 {
		// Without a durable backup phase this token never renamed the target,
		// whatever the target is now; conclude it without touching files.
		return w.saveNFOCommitRollback(ctx, lease, record, repository, input)
	}
	scope := input.task.Preparation.Scope
	releaseIO, err := w.budget.Acquire(ctx, app.WorkIO)
	if err != nil {
		return input.settlement.Phase, err
	}
	defer releaseIO()
	if info, err := os.Lstat(scope.Source.RootPath); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return input.settlement.Phase, ErrChanged
	}
	root, err := os.OpenRoot(filepath.Clean(scope.Source.RootPath) + string(os.PathSeparator) + ".")
	if err != nil {
		return input.settlement.Phase, ErrChanged
	}
	defer root.Close()
	parts := strings.Split(scope.Source.RelativePath, "/")
	for i := range parts[:len(parts)-1] {
		info, err := root.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return input.settlement.Phase, ErrChanged
		}
	}
	directory, err := root.OpenRoot(filepath.Dir(filepath.FromSlash(scope.Source.RelativePath)))
	if err != nil {
		return input.settlement.Phase, ErrChanged
	}
	defer directory.Close()
	// The primitive rechecks the planned parent identity under the native lock.
	phase, err := abortNFOCommitFiles(ctx, directory, input.files, input.task.Preparation.Original, input.task.Preparation.Replacement, ops)
	if phase != nfoSettleRolledBack {
		if err == nil {
			err = ErrRollback
		}
		return input.settlement.Phase, err
	}
	return w.saveNFOCommitRollback(ctx, lease, record, repository, input)
}

func (w *Writer) saveNFOCommitRollback(ctx context.Context, lease domain.JobLease, record domain.NFOWriteCommitRecord, repository app.NFOWriteCommitSettlementRepository, input nfoCommitSettlementInput) (domain.NFOWriteCommitSettlementPhase, error) {
	saved, err := repository.SaveNFOWriteCommitSettlement(ctx, lease, record.Sequence, record.Token, input.attempt, domain.NFOWriteCommitRolledBack)
	if err != nil {
		return input.settlement.Phase, err
	}
	if saved.Phase != domain.NFOWriteCommitRolledBack || saved.Attempt != input.attempt {
		return saved.Phase, ErrChanged
	}
	return domain.NFOWriteCommitRolledBack, nil
}
