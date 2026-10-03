package nfo

import (
	"context"
	"crypto/sha256"
	"os"
)

const maxNFOCommitAttempts uint8 = 3

// One reservation covers all three attempts before any file side effect. Names
// and hashes are private immutable intent; they never authorize a filesystem
// operation, ownership of an existing object, recovery or journal settlement.
type nfoCommitAttemptReservation struct {
	plan                            nfoCommitFilePlan
	originalBytes, replacementBytes int64
	originalHash, replacementHash   [32]byte
	retainedBytes                   int64
}

func (nfoCommitAttemptReservation) String() string   { return "nfo attempt reservation (redacted)" }
func (nfoCommitAttemptReservation) GoString() string { return "nfo attempt reservation (redacted)" }

type nfoCommitAttemptPersistence struct {
	// Save must durably retain the first reservation, charge its bounded capacity,
	// fence the current authority and return that exact first value on replay.
	// An unknown response prevents file side effects; the caller must reread it.
	reserve  func(context.Context, nfoCommitAttemptReservation) (nfoCommitAttemptReservation, error)
	progress func(context.Context, nfoCommitFiles) error
	ready    func(context.Context, nfoCommitFiles) error
	resume   *nfoCommitFiles
}

// prepareNFOCommitAttempt uses another already-reserved namespace after an
// unproven create. It never inspects, adopts, removes or changes earlier attempts.
// Allocation/selection, global reservation, recovery lease and cleanup require
// the later persistent protocol; Stage has no caller for this private primitive.
func prepareNFOCommitAttempt(ctx context.Context, directory *os.Root, filename string, original, replacement *Document, token [16]byte, attempt uint8, persist nfoCommitAttemptPersistence, ops nfoWriteOperations) (*nfoCommitFiles, error) {
	if attempt < 1 || attempt > maxNFOCommitAttempts || original == nil || replacement == nil || persist.reserve == nil || persist.progress == nil || persist.ready == nil {
		return nil, ErrInvalidInput
	}
	if len(original.original) == 0 || len(replacement.original) == 0 || int64(len(original.original)) > MaxAllowedBytes || int64(len(replacement.original)) > MaxAllowedBytes {
		return nil, ErrInvalidInput
	}
	originalHash, replacementHash := sha256.Sum256(original.original), sha256.Sum256(replacement.original)
	originalBytes, replacementBytes := int64(len(original.original)), int64(len(replacement.original))
	// Five names contain two new regular files and three hardlinks. Conservatively
	// charge every planned name independently, including the original witness.
	retainedBytes := int64(maxNFOCommitAttempts) * (3*originalBytes + 2*replacementBytes)
	return prepareNFOCommitFiles(ctx, directory, filename, original, replacement, token, nfoCommitFilePersistence{
		attempt: attempt, resume: persist.resume, progress: persist.progress, ready: persist.ready,
		plan: func(ctx context.Context, plan nfoCommitFilePlan) error {
			value := nfoCommitAttemptReservation{plan: plan, originalBytes: originalBytes, replacementBytes: replacementBytes, originalHash: originalHash, replacementHash: replacementHash, retainedBytes: retainedBytes}
			first, err := persist.reserve(ctx, value)
			if err != nil {
				return err
			}
			if first != value {
				return ErrChanged
			}
			return nil
		},
	}, ops)
}
