package postgres

import (
	"context"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Session advisory locks that keep a cluster-wide background job (G41.1)
// to one instance at a time. They are distinct from the locks the work
// itself takes inside its transactions (watchStatsLock): a run holds its
// lock on a pooled connection of its own while it uses other connections.
const (
	webhookDeliveryLock       = 17481268
	watchStatsAggregationLock = 17481269
	subtitleOCRLock           = 17481270
)

func backgroundLockKey(kind string) (int32, bool) {
	switch kind {
	case domain.JobWebhookDelivery:
		return webhookDeliveryLock, true
	case domain.JobWatchStatsAggregation:
		return watchStatsAggregationLock, true
	case domain.JobSubtitleOCR:
		return subtitleOCRLock, true
	}
	return 0, false
}

// backgroundUnlockTimeout bounds the unlock; it runs even when the run was
// cancelled.
const backgroundUnlockTimeout = 5 * time.Second

// TryBackgroundLock takes the session advisory lock of a cluster-wide
// background kind without waiting. While acquired, the lock keeps one
// pooled connection until release. A connection whose unlock fails is
// closed instead of returning to the pool, which releases the lock with the
// session; so does a crashed instance.
func (s *Store) TryBackgroundLock(ctx context.Context, kind string) (func(), bool, error) {
	key, ok := backgroundLockKey(kind)
	if !ok {
		return nil, false, domain.ErrInvalid
	}
	pooled, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, storageError(err)
	}
	var locked bool
	if err = pooled.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext(current_schema()),$1)`, key).Scan(&locked); err != nil {
		// The statement may have taken the lock before the error; the
		// session must not return to the pool.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundUnlockTimeout)
		_ = pooled.Conn().Close(closeCtx)
		cancel()
		pooled.Release()
		return nil, false, storageError(err)
	}
	if !locked {
		pooled.Release()
		return nil, false, nil
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundUnlockTimeout)
			defer cancel()
			var unlocked bool
			if err := pooled.QueryRow(unlockCtx, `SELECT pg_advisory_unlock(hashtext(current_schema()),$1)`, key).Scan(&unlocked); err != nil || !unlocked {
				_ = pooled.Conn().Close(unlockCtx)
			}
			pooled.Release()
		})
	}
	return release, true, nil
}
