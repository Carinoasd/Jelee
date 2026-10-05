package nfo

import (
	"context"
	"errors"
	"os"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ErrCommitAttemptsExhausted is a bounded refusal: every reserved namespace may
// hold an unknown object, and none of them is inspected, adopted or removed.
var ErrCommitAttemptsExhausted = errors.New("nfo_commit_attempts_exhausted")

// stageNFOCommitAttempts chooses one namespace from persisted evidence before
// any file side effect. A saved checkpoint pins its attempt: a changed first
// output is refused, never rotated. Only an attempt without a first checkpoint
// whose names already exist moves to a newly allocated namespace, and earlier
// names stay untouched as unknown objects.
func stageNFOCommitAttempts(ctx context.Context, directory *os.Root, filename string, original, replacement *Document, token [16]byte, lease domain.JobLease, record domain.NFOWriteCommitRecord, evidence domain.NFOWriteCommitFileEvidence, repository app.NFOWriteCommitAttemptStageRepository, check func(context.Context) error, ops nfoWriteOperations) error {
	attempts, err := repository.GetNFOWriteCommitAttempts(ctx, lease, record.Sequence, record.Token)
	if err != nil {
		return err
	}
	latest := uint8(0)
	for i, entry := range attempts.Attempts {
		if entry.Number == 0 {
			break
		}
		if int(entry.Number) != i+1 || !attempts.ReservationRecorded || !evidence.PlanRecorded {
			return ErrChanged
		}
		latest = entry.Number
	}
	if evidence.ReadyRecorded {
		// Legacy ready and an allocated namespace are mutually exclusive.
		if latest != 0 {
			return ErrChanged
		}
		return stageLegacyNFOCommitFiles(ctx, directory, filename, original, replacement, token, lease, record, evidence, repository, check, ops)
	}
	if latest != 0 && evidence.CheckpointRecorded {
		return ErrChanged
	}
	for _, entry := range attempts.Attempts[:latest] {
		if entry.ReadyRecorded {
			if entry.Number != latest {
				return ErrChanged
			}
			return resumeNFOCommitAttemptReady(ctx, directory, filename, original, replacement, token, lease, record, evidence, entry, repository, check)
		}
	}
	if latest == 0 {
		if evidence.CheckpointRecorded || !evidence.PlanRecorded {
			return stageLegacyNFOCommitFiles(ctx, directory, filename, original, replacement, token, lease, record, evidence, repository, check, ops)
		}
		// A recorded plan without a checkpoint may have left legacy names from an
		// interrupted create. Retry them only when none exists.
		present, err := nfoCommitNamesPresent(directory, nfoCommitFilePlan{token: token})
		if err != nil {
			return err
		}
		if !present {
			return stageLegacyNFOCommitFiles(ctx, directory, filename, original, replacement, token, lease, record, evidence, repository, check, ops)
		}
	} else {
		current := attempts.Attempts[latest-1]
		if current.CheckpointRecorded {
			return stageNFOCommitAttempt(ctx, directory, filename, original, replacement, token, lease, record, evidence, latest, &current.Checkpoint, repository, ops)
		}
		present, err := nfoCommitNamesPresent(directory, nfoCommitFilePlan{token: token, attempt: latest})
		if err != nil {
			return err
		}
		if !present {
			return stageNFOCommitAttempt(ctx, directory, filename, original, replacement, token, lease, record, evidence, latest, nil, repository, ops)
		}
	}
	if latest >= maxNFOCommitAttempts {
		return ErrCommitAttemptsExhausted
	}
	// Durable allocation precedes every side effect in the new namespace.
	next, err := repository.AllocateNFOWriteCommitAttempt(ctx, lease, record.Sequence, record.Token, latest)
	if err != nil {
		return err
	}
	if next != latest+1 {
		return ErrChanged
	}
	present, err := nfoCommitNamesPresent(directory, nfoCommitFilePlan{token: token, attempt: next})
	if err != nil {
		return err
	}
	if present {
		// The namespace was never used by this token; a name there is foreign.
		return ErrChanged
	}
	return stageNFOCommitAttempt(ctx, directory, filename, original, replacement, token, lease, record, evidence, next, nil, repository, ops)
}

// nfoCommitNamesPresent reports whether any name of the namespace may exist.
// An unclear observation counts as present so its object is retained.
func nfoCommitNamesPresent(directory *os.Root, plan nfoCommitFilePlan) (bool, error) {
	for _, name := range plan.names() {
		if _, err := directory.Lstat(name); err == nil || !errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
	}
	return false, nil
}

func stageNFOCommitAttempt(ctx context.Context, directory *os.Root, filename string, original, replacement *Document, token [16]byte, lease domain.JobLease, record domain.NFOWriteCommitRecord, evidence domain.NFOWriteCommitFileEvidence, attempt uint8, saved *domain.NFOWriteCommitFileCheckpoint, repository app.NFOWriteCommitAttemptStageRepository, ops nfoWriteOperations) error {
	check := ops.checkSource
	persist := nfoCommitAttemptPersistence{
		reserve: func(ctx context.Context, value nfoCommitAttemptReservation) (nfoCommitAttemptReservation, error) {
			if err := check(ctx); err != nil {
				return nfoCommitAttemptReservation{}, err
			}
			plan := domain.NFOWriteCommitFilePlan{Version: value.plan.version, TargetName: value.plan.filename, ParentIdentity: value.plan.parent.record, TargetIdentity: value.plan.target.record}
			if !evidence.PlanRecorded || plan != evidence.Plan {
				return nfoCommitAttemptReservation{}, ErrChanged
			}
			if replay, err := repository.SaveNFOWriteCommitFilePlan(ctx, lease, record.Sequence, record.Token, plan); err != nil || replay != plan {
				return nfoCommitAttemptReservation{}, ErrChanged
			}
			first, err := repository.ReserveNFOWriteCommitAttempts(ctx, lease, record.Sequence, record.Token, domain.NFOWriteCommitAttemptReservation{OriginalBytes: value.originalBytes, ReplacementBytes: value.replacementBytes, OriginalHash: value.originalHash, ReplacementHash: value.replacementHash, RetainedBytes: value.retainedBytes})
			if err != nil {
				return nfoCommitAttemptReservation{}, err
			}
			value.originalBytes, value.replacementBytes, value.originalHash, value.replacementHash, value.retainedBytes = first.OriginalBytes, first.ReplacementBytes, first.OriginalHash, first.ReplacementHash, first.RetainedBytes
			return value, nil
		},
		progress: func(ctx context.Context, files nfoCommitFiles) error {
			if err := check(ctx); err != nil {
				return err
			}
			value := domain.NFOWriteCommitFileCheckpoint{Phase: 1, OutputIdentity: files.output.record}
			if files.rollback != (nfoNativeIdentity{}) {
				value.Phase, value.RollbackIdentity = 2, files.rollback.record
			}
			first, err := repository.SaveNFOWriteCommitAttemptCheckpoint(ctx, lease, record.Sequence, record.Token, attempt, value)
			if err != nil {
				return err
			}
			if first != value {
				return ErrChanged
			}
			return nil
		},
		ready: func(ctx context.Context, files nfoCommitFiles) error {
			if err := check(ctx); err != nil {
				return err
			}
			value := domain.NFOWriteCommitFilesReady{OutputIdentity: files.output.record, RollbackIdentity: files.rollback.record}
			first, err := repository.SaveNFOWriteCommitAttemptReady(ctx, lease, record.Sequence, record.Token, attempt, value)
			if err != nil {
				return err
			}
			if first != value {
				return ErrChanged
			}
			return nil
		},
	}
	if saved != nil {
		if !evidence.PlanRecorded || domain.ValidateNFOWriteCommitFileCheckpoint(*saved) != nil {
			return ErrChanged
		}
		persist.resume = &nfoCommitFiles{
			plan:   nfoCommitFilePlan{version: evidence.Plan.Version, token: token, filename: evidence.Plan.TargetName, parent: nfoNativeIdentity{evidence.Plan.ParentIdentity}, target: nfoNativeIdentity{evidence.Plan.TargetIdentity}, attempt: attempt},
			output: nfoNativeIdentity{saved.OutputIdentity}, rollback: nfoNativeIdentity{saved.RollbackIdentity},
		}
	}
	_, err := prepareNFOCommitAttempt(ctx, directory, filename, original, replacement, token, attempt, persist, ops)
	return err
}

// resumeNFOCommitAttemptReady verifies the selected attempt and replays its
// fenced records. It creates, adopts and removes nothing.
func resumeNFOCommitAttemptReady(ctx context.Context, directory *os.Root, filename string, original, replacement *Document, token [16]byte, lease domain.JobLease, record domain.NFOWriteCommitRecord, evidence domain.NFOWriteCommitFileEvidence, entry domain.NFOWriteCommitAttempt, repository app.NFOWriteCommitAttemptStageRepository, check func(context.Context) error) error {
	if !evidence.PlanRecorded || evidence.Plan.TargetName != filename || domain.ValidateNFOWriteCommitFilesReady(entry.Ready) != nil {
		return ErrChanged
	}
	files := nfoCommitFiles{
		plan:   nfoCommitFilePlan{version: evidence.Plan.Version, token: token, filename: evidence.Plan.TargetName, parent: nfoNativeIdentity{evidence.Plan.ParentIdentity}, target: nfoNativeIdentity{evidence.Plan.TargetIdentity}, attempt: entry.Number},
		output: nfoNativeIdentity{entry.Ready.OutputIdentity}, rollback: nfoNativeIdentity{entry.Ready.RollbackIdentity},
	}
	lock, err := lockNFOFile(ctx, directory, filename)
	if err != nil {
		return err
	}
	defer lock.Close()
	verify := func() error {
		if err := check(ctx); err != nil {
			return err
		}
		if !lock.check() {
			return ErrFileLock
		}
		return verifyNFOCommitFiles(ctx, directory, files, original.original, replacement.original)
	}
	if err := verify(); err != nil {
		return err
	}
	plan, err := repository.SaveNFOWriteCommitFilePlan(ctx, lease, record.Sequence, record.Token, evidence.Plan)
	if err != nil {
		return err
	}
	if plan != evidence.Plan {
		return ErrChanged
	}
	ready, err := repository.SaveNFOWriteCommitAttemptReady(ctx, lease, record.Sequence, record.Token, entry.Number, entry.Ready)
	if err != nil {
		return err
	}
	if ready != entry.Ready {
		return ErrChanged
	}
	return verify()
}
