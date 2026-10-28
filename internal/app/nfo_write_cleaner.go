package app

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOWriteCleanupRepository is the durable side of NFO write cleanup: the
// per-token cleanup lease, the identities to check and the recorded outcome,
// which releases the token's attempt reservation.
type NFOWriteCleanupRepository interface {
	ListNFOWriteCommitCleanups(context.Context, int) ([]string, error)
	AcquireNFOWriteCommitCleanup(context.Context, string, string, time.Duration) (domain.NFOWriteCommitCleanupLease, error)
	RenewNFOWriteCommitCleanup(context.Context, domain.NFOWriteCommitCleanupLease, time.Duration) (domain.NFOWriteCommitCleanupLease, error)
	GetNFOWriteCommitCleanupInput(context.Context, domain.NFOWriteCommitCleanupLease) (domain.NFOWriteCommitCleanupInput, error)
	CompleteNFOWriteCommitCleanup(context.Context, domain.NFOWriteCommitCleanupLease, domain.NFOWriteCommitCleanupOutcome, int) error
}

// NFOWriteCommitFileCleaner is the filesystem side. It removes only objects
// whose native identity matches the input and reports how many it kept.
type NFOWriteCommitFileCleaner interface {
	CleanNFOWriteCommitFiles(context.Context, domain.NFOWriteCommitCleanupInput) (domain.NFOWriteCommitCleanupOutcome, int, error)
}

// NFOWriteCleaner removes the files resolved NFO write tokens retained and
// releases their attempt capacity. It holds no state between calls.
type NFOWriteCleaner struct {
	repository NFOWriteCleanupRepository
	files      NFOWriteCommitFileCleaner
	owner      string
	lease      time.Duration
}

// NewNFOWriteCleaner binds the cleaner to its repository, filesystem side,
// owner (a UUID) and lease TTL, which it renews at a third.
func NewNFOWriteCleaner(repository NFOWriteCleanupRepository, files NFOWriteCommitFileCleaner, owner string, lease time.Duration) (*NFOWriteCleaner, error) {
	if repository == nil || files == nil || !domain.ValidID(owner) || lease < 3*time.Second || lease > time.Hour {
		return nil, domain.ErrInvalid
	}
	return &NFOWriteCleaner{repository: repository, files: files, owner: owner, lease: lease}, nil
}

// CleanPending cleans up to limit tokens. A token whose lease is held
// elsewhere is skipped. It returns the cleaned count and the first error,
// after attempting every listed token.
func (c *NFOWriteCleaner) CleanPending(ctx context.Context, limit int) (int, error) {
	if c == nil || ctx == nil {
		return 0, domain.ErrInvalid
	}
	tokens, err := c.repository.ListNFOWriteCommitCleanups(ctx, limit)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	var first error
	for _, token := range tokens {
		if err := ctx.Err(); err != nil {
			return cleaned, err
		}
		_, _, err := c.Clean(ctx, token)
		if errors.Is(err, domain.ErrConflict) {
			continue
		}
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		cleaned++
	}
	return cleaned, first
}

// Clean takes the cleanup lease of one token, renews it while working,
// removes the token's retained objects and records the outcome. An error
// leaves the token for a later pass; the filesystem pass is idempotent.
func (c *NFOWriteCleaner) Clean(ctx context.Context, token string) (domain.NFOWriteCommitCleanupOutcome, int, error) {
	if c == nil || ctx == nil || !domain.ValidID(token) {
		return 0, 0, domain.ErrInvalid
	}
	lease, err := c.repository.AcquireNFOWriteCommitCleanup(ctx, token, c.owner, c.lease)
	if err != nil {
		return 0, 0, err
	}
	work, cancel := context.WithCancelCause(ctx)
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		for {
			timer := time.NewTimer(c.lease / 3)
			select {
			case <-work.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if _, err := c.repository.RenewNFOWriteCommitCleanup(work, lease, c.lease); err != nil {
				cancel(err)
				return
			}
		}
	}()
	defer func() {
		cancel(nil)
		<-renewed
	}()
	outcome, retained, err := c.clean(work, lease)
	if err != nil {
		if cause := context.Cause(work); cause != nil && ctx.Err() == nil && !errors.Is(cause, context.Canceled) {
			return 0, 0, cause
		}
		return 0, 0, err
	}
	return outcome, retained, nil
}

func (c *NFOWriteCleaner) clean(ctx context.Context, lease domain.NFOWriteCommitCleanupLease) (domain.NFOWriteCommitCleanupOutcome, int, error) {
	input, err := c.repository.GetNFOWriteCommitCleanupInput(ctx, lease)
	if err != nil {
		return 0, 0, err
	}
	outcome, retained, err := c.files.CleanNFOWriteCommitFiles(ctx, input)
	if err != nil {
		return 0, 0, err
	}
	if err := c.repository.CompleteNFOWriteCommitCleanup(ctx, lease, outcome, retained); err != nil {
		return 0, 0, err
	}
	return outcome, retained, nil
}
