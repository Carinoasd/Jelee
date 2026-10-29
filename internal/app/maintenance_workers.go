package app

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// SubtitleCacheSweeper maintains Jelee's own extraction cache (the
// matroska adapter implements it). Source directories are named by media
// source ID. Only an unusable cache root is an error; a revision that could
// not be removed is counted in SubtitleCacheSweep.Failed.
type SubtitleCacheSweeper interface {
	// CountSources counts the source directories.
	CountSources(ctx context.Context) (int64, error)
	// ListSources returns up to limit source directory names after after
	// in ascending order.
	ListSources(ctx context.Context, after string, limit int) ([]string, error)
	// SweepSource removes the whole directory of a source that no longer
	// exists; for a live one it removes revisions last used before
	// idleBefore, unreadable revisions and crashed staging directories.
	SweepSource(ctx context.Context, source string, live bool, idleBefore time.Time) (domain.SubtitleCacheSweep, error)
	// EnforceCapacity removes least recently used revisions until the cache
	// fits its size bound.
	EnforceCapacity(ctx context.Context) (domain.SubtitleCacheSweep, error)
}

// SubtitleCacheCleanupExecutionRepository is the durable side of a
// subtitle_cache_cleanup run; every method is fenced by the job lease.
type SubtitleCacheCleanupExecutionRepository interface {
	// NextSubtitleCacheCleanup returns the job state and which of sources
	// still exist, and records total as the progress total unless one is
	// set (-1 records nothing). It answers context.Canceled once
	// cancellation was requested.
	NextSubtitleCacheCleanup(ctx context.Context, lease domain.JobLease, sources []string, total int64) (domain.SubtitleCacheCleanupBatch, error)
	RecordSubtitleCacheSources(ctx context.Context, lease domain.JobLease, cursor string, finished, failed int, sweep domain.SubtitleCacheSweep, last bool) error
	RecordSubtitleCacheCapacity(ctx context.Context, lease domain.JobLease, sweep domain.SubtitleCacheSweep) error
	FinishSubtitleCacheCleanup(ctx context.Context, lease domain.JobLease, state, code string) error
}

// SubtitleCacheCleanupWorker runs one subtitle_cache_cleanup job: it walks
// the source directories in name order, checkpoints after every batch, then
// enforces the size bound once.
type SubtitleCacheCleanupWorker struct {
	sweeper   SubtitleCacheSweeper
	repo      SubtitleCacheCleanupExecutionRepository
	dbTimeout time.Duration
	now       func() time.Time
}

// NewSubtitleCacheCleanupWorker accepts a nil sweeper: every job it runs then
// fails with domain.ErrSubtitleCacheUnavailable, so a job queued under
// another configuration ends instead of blocking its library.
func NewSubtitleCacheCleanupWorker(sweeper SubtitleCacheSweeper, repository SubtitleCacheCleanupExecutionRepository, dbTimeout time.Duration) (*SubtitleCacheCleanupWorker, error) {
	if repository == nil || dbTimeout < 0 {
		return nil, domain.ErrInvalid
	}
	if dbTimeout == 0 {
		dbTimeout = 2 * time.Second
	}
	return &SubtitleCacheCleanupWorker{sweeper: sweeper, repo: repository, dbTimeout: dbTimeout, now: time.Now}, nil
}

func dbCall(ctx context.Context, timeout time.Duration, call func(context.Context) error) error {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return call(c)
}

// Run cleans from the stored cursor on. Cancellation stops between sources
// after recording the finished ones; a resumed lease continues after the
// cursor, and repeating a source is harmless because removal is idempotent.
func (w *SubtitleCacheCleanupWorker) Run(ctx context.Context, lease domain.JobLease) error {
	if ctx == nil || lease.Job.Kind != domain.JobSubtitleCacheCleanup {
		return domain.ErrInvalid
	}
	if w.sweeper == nil {
		return domain.ErrSubtitleCacheUnavailable
	}
	total, err := w.sweeper.CountSources(ctx)
	if err != nil {
		return err
	}
	var batch domain.SubtitleCacheCleanupBatch
	if err = dbCall(ctx, w.dbTimeout, func(c context.Context) (err error) {
		batch, err = w.repo.NextSubtitleCacheCleanup(c, lease, nil, total)
		return err
	}); err != nil {
		return err
	}
	idleBefore := w.now().Add(-time.Duration(batch.State.MaxIdleDays) * 24 * time.Hour)
	for batch.State.Phase == domain.SubtitleCachePhaseSources {
		cursor := ""
		if batch.State.CursorSource != nil {
			cursor = *batch.State.CursorSource
		}
		names, err := w.sweeper.ListSources(ctx, cursor, domain.SubtitleCacheBatchSize)
		if err != nil {
			return err
		}
		if err = dbCall(ctx, w.dbTimeout, func(c context.Context) (err error) {
			batch, err = w.repo.NextSubtitleCacheCleanup(c, lease, names, -1)
			return err
		}); err != nil {
			return err
		}
		var sweep domain.SubtitleCacheSweep
		finished, failed, last := 0, 0, ""
		var stop error
		for _, name := range names {
			if stop = ctx.Err(); stop != nil {
				break
			}
			result, err := w.sweeper.SweepSource(ctx, name, batch.Live[name], idleBefore)
			if err != nil {
				stop = err
				break
			}
			sweep.Add(result)
			if result.Failed > 0 {
				failed++
			} else {
				finished++
			}
			last = name
		}
		// Finished sources are recorded even when the run stops right after.
		if err = dbCall(context.WithoutCancel(ctx), w.dbTimeout, func(c context.Context) error {
			return w.repo.RecordSubtitleCacheSources(c, lease, last, finished, failed, sweep, stop == nil && len(names) < domain.SubtitleCacheBatchSize)
		}); err != nil {
			return err
		}
		if stop != nil {
			return stop
		}
		if len(names) < domain.SubtitleCacheBatchSize {
			batch.State.Phase = domain.SubtitleCachePhaseCapacity
			break
		}
		if err = dbCall(ctx, w.dbTimeout, func(c context.Context) (err error) {
			batch, err = w.repo.NextSubtitleCacheCleanup(c, lease, nil, -1)
			return err
		}); err != nil {
			return err
		}
	}
	if batch.State.Phase != domain.SubtitleCachePhaseCapacity {
		return nil
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	sweep, err := w.sweeper.EnforceCapacity(ctx)
	if err != nil {
		return err
	}
	return dbCall(context.WithoutCancel(ctx), w.dbTimeout, func(c context.Context) error {
		return w.repo.RecordSubtitleCacheCapacity(c, lease, sweep)
	})
}

// BackupReminderExecutionRepository is the durable side of a backup_reminder
// run.
type BackupReminderExecutionRepository interface {
	// CheckBackupReminder evaluates and records the result once; a resumed
	// run returns the recorded result. It answers context.Canceled once
	// cancellation was requested.
	CheckBackupReminder(ctx context.Context, lease domain.JobLease) (domain.BackupStatus, error)
	FinishBackupReminder(ctx context.Context, lease domain.JobLease, state, code string) error
}

// BackupReminderWorker runs one backup_reminder job. It never backs anything
// up: an overdue or missing metadata export becomes a backup.reminder audit
// entry and the job's result.
type BackupReminderWorker struct {
	repo      BackupReminderExecutionRepository
	dbTimeout time.Duration
}

// NewBackupReminderWorker binds the repository; dbTimeout zero uses two
// seconds.
func NewBackupReminderWorker(repository BackupReminderExecutionRepository, dbTimeout time.Duration) (*BackupReminderWorker, error) {
	if repository == nil || dbTimeout < 0 {
		return nil, domain.ErrInvalid
	}
	if dbTimeout == 0 {
		dbTimeout = 2 * time.Second
	}
	return &BackupReminderWorker{repo: repository, dbTimeout: dbTimeout}, nil
}

// Run records the backup status.
func (w *BackupReminderWorker) Run(ctx context.Context, lease domain.JobLease) error {
	if ctx == nil || lease.Job.Kind != domain.JobBackupReminder {
		return domain.ErrInvalid
	}
	return dbCall(ctx, w.dbTimeout, func(c context.Context) error {
		_, err := w.repo.CheckBackupReminder(c, lease)
		return err
	})
}

// ExternalMetadataRemovalExecutionRepository is the durable side of an
// external_metadata_removal run; every method is fenced by the job lease.
type ExternalMetadataRemovalExecutionRepository interface {
	// NextExternalMetadataRemovalBatch returns candidate items after the
	// cursor. It answers context.Canceled once cancellation was requested
	// and domain.ErrForbidden when the submitter is no longer an active
	// administrator.
	NextExternalMetadataRemovalBatch(ctx context.Context, lease domain.JobLease, limit int) ([]string, error)
	// RemoveExternalMetadataForJob removes and checkpoints one item in one
	// transaction.
	RemoveExternalMetadataForJob(ctx context.Context, lease domain.JobLease, item string) (domain.ExternalMetadataRemovalOutcome, error)
	// RecordExternalMetadataRemovalFailure counts an item that could not be
	// changed and moves the cursor past it.
	RecordExternalMetadataRemovalFailure(ctx context.Context, lease domain.JobLease, item string) error
	FinishExternalMetadataRemoval(ctx context.Context, lease domain.JobLease, state, code string) error
}

// ExternalMetadataRemovalWorker runs one external_metadata_removal job.
type ExternalMetadataRemovalWorker struct {
	repo      ExternalMetadataRemovalExecutionRepository
	dbTimeout time.Duration
}

// NewExternalMetadataRemovalWorker binds the repository; dbTimeout zero uses
// two seconds.
func NewExternalMetadataRemovalWorker(repository ExternalMetadataRemovalExecutionRepository, dbTimeout time.Duration) (*ExternalMetadataRemovalWorker, error) {
	if repository == nil || dbTimeout < 0 {
		return nil, domain.ErrInvalid
	}
	if dbTimeout == 0 {
		dbTimeout = 2 * time.Second
	}
	return &ExternalMetadataRemovalWorker{repo: repository, dbTimeout: dbTimeout}, nil
}

// errExternalRemovalFailures ends a run after too many failed items in a row.
var errExternalRemovalFailures = errors.New("external metadata removal failed for consecutive items")

// Run removes item by item after the stored cursor until none remain. An
// item whose change is refused on its own (domain.ErrInvalid or
// domain.ErrConflict, for example a constraint) is rolled back, counted as
// failed and passed; cancellation, lease loss, lost administrator rights and
// storage failures stop the run with every finished item kept, and so do
// domain.ExternalMetadataRemovalMaxConsecutiveFailures failed items in a row.
func (w *ExternalMetadataRemovalWorker) Run(ctx context.Context, lease domain.JobLease) error {
	if ctx == nil || lease.Job.Kind != domain.JobExternalMetadataRemoval {
		return domain.ErrInvalid
	}
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var items []string
		if err := dbCall(ctx, w.dbTimeout, func(c context.Context) (err error) {
			items, err = w.repo.NextExternalMetadataRemovalBatch(c, lease, domain.ExternalMetadataRemovalBatchSize)
			return err
		}); err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := dbCall(context.WithoutCancel(ctx), w.dbTimeout, func(c context.Context) error {
				_, err := w.repo.RemoveExternalMetadataForJob(c, lease, item)
				return err
			})
			if err == nil {
				failures = 0
				continue
			}
			if !errors.Is(err, domain.ErrInvalid) && !errors.Is(err, domain.ErrConflict) {
				return err
			}
			if err = dbCall(context.WithoutCancel(ctx), w.dbTimeout, func(c context.Context) error {
				return w.repo.RecordExternalMetadataRemovalFailure(c, lease, item)
			}); err != nil {
				return err
			}
			if failures++; failures >= domain.ExternalMetadataRemovalMaxConsecutiveFailures {
				return errExternalRemovalFailures
			}
		}
	}
}
