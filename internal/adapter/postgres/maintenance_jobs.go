package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
	"github.com/jackc/pgx/v5"
)

var (
	_ app.MaintenanceJobRepository                   = (*Store)(nil)
	_ app.SubtitleCacheCleanupExecutionRepository    = (*Store)(nil)
	_ app.BackupReminderExecutionRepository          = (*Store)(nil)
	_ app.BackupReminderScheduleRepository           = (*Store)(nil)
	_ app.ExternalMetadataRemovalExecutionRepository = (*Store)(nil)
)

// insertKindJob creates a job of kind. total, when not nil, is its progress
// total at admission.
func insertKindJob(ctx context.Context, tx pgx.Tx, library string, actor *string, key, kind, priority string, policy domain.JobPolicy, total *int64) (domain.Job, error) {
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,progress_total,progress_total_at)
 VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::bigint,CASE WHEN $13::bigint IS NULL THEN NULL ELSE clock_timestamp() END) RETURNING `+jobColumns,
		library, actor, key, kind, priority, policy.QueueLimit, policy.HistoryLimit, policy.MaxEntries, policy.MaxDirectories, policy.MaxAttempts, policy.MissingCountLimit, policy.MissingPercentLimit, total))
	if err != nil {
		return domain.Job{}, err
	}
	tracing.Remember(ctx, tracing.LinkJob, job.ID)
	return job, nil
}

// replayKindJob returns the actor's job of the same key when it is a job of
// kind and priority (and library, when one is named); another job under the
// key is domain.ErrConflict. found is false without a job under the key.
func replayKindJob(ctx context.Context, tx pgx.Tx, actor domain.Actor, key, kind, library, priority string) (domain.Job, bool, error) {
	old, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE actor_id=$1 AND idempotency_key=$2`, actor.UserID, key))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Job{}, false, nil
	}
	if err != nil {
		return domain.Job{}, false, err
	}
	if old.Kind != kind || library != "" && old.LibraryID != library || old.Priority != priority {
		return domain.Job{}, false, domain.ErrConflict
	}
	return old, true, probeAdminStillLive(ctx, tx, actor)
}

// anchorInstanceJob picks the library an instance-wide job of kind is queued
// under: the first library without an active job. Only one such job of a kind
// may be active. A server without libraries is domain.ErrConflict; one where
// every library is busy, or a kind already active, is domain.ErrJobBusy. The
// caller holds the jobs advisory lock, which serializes admissions.
func anchorInstanceJob(ctx context.Context, tx pgx.Tx, kind string, policy domain.JobPolicy) (string, error) {
	var active, libraries bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE kind=$1 AND state IN ('queued','running')),EXISTS(SELECT 1 FROM libraries)`, kind).Scan(&active, &libraries); err != nil {
		return "", storageError(err)
	}
	if !libraries {
		return "", domain.ErrConflict
	}
	if active {
		return "", domain.ErrJobBusy
	}
	var library string
	err := tx.QueryRow(ctx, `SELECT l.id::text FROM libraries l WHERE NOT EXISTS(SELECT 1 FROM jobs j WHERE j.library_id=l.id AND j.state IN ('queued','running')) ORDER BY l.id LIMIT 1 FOR SHARE OF l`).Scan(&library)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrJobBusy
	}
	if err != nil {
		return "", storageError(err)
	}
	return library, libraryAdmission(ctx, tx, library, policy)
}

// fencedKind fences the lease and requires a job of kind.
func fencedKind(ctx context.Context, tx pgx.Tx, l domain.JobLease, kind string) (domain.JobLease, error) {
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.JobLease{}, err
	}
	if current.Job.Kind != kind {
		return domain.JobLease{}, domain.ErrInvalid
	}
	return current, nil
}

// finishKindJob makes a job of kind terminal under its lease fence; failed
// accepts the kind's own failure code besides the shared ones.
func (s *Store) finishKindJob(ctx context.Context, l domain.JobLease, kind, failed, state, code string, after func(pgx.Tx, domain.JobLease) error) error {
	if !validJobError(state, code) && !(state == domain.JobFailed && code == failed) {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedKind(ctx, tx, l, kind)
	if err != nil {
		return err
	}
	if state == domain.JobSucceeded && current.Job.CancelRequested {
		return domain.ErrConflict
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET state=$2,error_code=$3,finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE id=$1::uuid`, l.Job.ID, state, code); err != nil {
		return err
	}
	if after != nil {
		if err = after(tx, current); err != nil {
			return err
		}
	}
	if err = trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// readState runs a fixed state query for an administrator; a job of another
// kind has no state row and is domain.ErrNotFound.
func (s *Store) readState(ctx context.Context, actor domain.Actor, job string, read func(pgx.Tx) error) error {
	if ctx == nil {
		return domain.ErrInvalid
	}
	if !domain.ValidID(job) {
		return domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = read(tx); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// --- subtitle_cache_cleanup ---

// SubmitSubtitleCacheCleanupJob admits a cleanup of the extraction cache
// under the first idle library. Replay of the same key and priority returns
// the first job.
func (s *Store) SubmitSubtitleCacheCleanupJob(ctx context.Context, actor domain.Actor, key, priority string, maxIdleDays int, policy domain.JobPolicy) (domain.Job, bool, error) {
	if ctx == nil || !validJobKey(key) || !validJobPolicy(policy) || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) || maxIdleDays < 1 || maxIdleDays > domain.SubtitleCacheIdleDaysMax {
		return domain.Job{}, false, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	if old, found, err := replayKindJob(ctx, tx, actor, key, domain.JobSubtitleCacheCleanup, "", priority); err != nil || found {
		if err != nil {
			return domain.Job{}, false, err
		}
		return old, true, storageError(tx.Commit(ctx))
	}
	library, err := anchorInstanceJob(ctx, tx, domain.JobSubtitleCacheCleanup, policy)
	if err != nil {
		return domain.Job{}, false, err
	}
	job, err := insertKindJob(ctx, tx, library, &actor.UserID, key, domain.JobSubtitleCacheCleanup, priority, policy, nil)
	if err != nil {
		return domain.Job{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO subtitle_cache_cleanup_job_state(job_id,max_idle_days,updated_at) VALUES($1::uuid,$2,clock_timestamp())`, job.ID, maxIdleDays); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "subtitle_cache_cleanup.submitted", job.ID, nil, map[string]any{"anchorLibraryId": library, "priority": priority, "maxIdleDays": maxIdleDays}); err != nil {
		return domain.Job{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, storageError(tx.Commit(ctx))
}

const subtitleCacheStateColumns = `job_id::text,max_idle_days,phase,cursor_source::text,sources,orphaned,revisions,capacity,failed,freed_bytes,updated_at`

func scanSubtitleCacheState(row pgx.Row) (domain.SubtitleCacheCleanupJobState, error) {
	var v domain.SubtitleCacheCleanupJobState
	if err := row.Scan(&v.JobID, &v.MaxIdleDays, &v.Phase, &v.CursorSource, &v.Sources, &v.Orphaned, &v.Revisions, &v.Capacity, &v.Failed, &v.FreedBytes, &v.UpdatedAt); err != nil {
		return v, storageError(err)
	}
	v.UpdatedAt = v.UpdatedAt.UTC()
	return v, nil
}

// GetSubtitleCacheCleanupState reads the progress of a cleanup job.
func (s *Store) GetSubtitleCacheCleanupState(ctx context.Context, actor domain.Actor, job string) (domain.SubtitleCacheCleanupJobState, error) {
	var v domain.SubtitleCacheCleanupJobState
	err := s.readState(ctx, actor, job, func(tx pgx.Tx) (err error) {
		v, err = scanSubtitleCacheState(tx.QueryRow(ctx, `SELECT `+subtitleCacheStateColumns+` FROM subtitle_cache_cleanup_job_state WHERE job_id=$1::uuid`, job))
		return err
	})
	return v, err
}

// NextSubtitleCacheCleanup implements
// app.SubtitleCacheCleanupExecutionRepository: the job's state, which of
// sources are media sources that still exist, and the progress total once.
// It answers context.Canceled once cancellation was requested.
func (s *Store) NextSubtitleCacheCleanup(ctx context.Context, lease domain.JobLease, sources []string, total int64) (domain.SubtitleCacheCleanupBatch, error) {
	if ctx == nil || len(sources) > 1000 || total < -1 {
		return domain.SubtitleCacheCleanupBatch{}, domain.ErrInvalid
	}
	for _, id := range sources {
		if !domain.ValidID(id) {
			return domain.SubtitleCacheCleanupBatch{}, domain.ErrInvalid
		}
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.SubtitleCacheCleanupBatch{}, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedKind(ctx, tx, lease, domain.JobSubtitleCacheCleanup)
	if err != nil {
		return domain.SubtitleCacheCleanupBatch{}, err
	}
	if current.Job.CancelRequested {
		return domain.SubtitleCacheCleanupBatch{}, context.Canceled
	}
	batch := domain.SubtitleCacheCleanupBatch{Live: map[string]bool{}}
	if batch.State, err = scanSubtitleCacheState(tx.QueryRow(ctx, `SELECT `+subtitleCacheStateColumns+` FROM subtitle_cache_cleanup_job_state WHERE job_id=$1::uuid`, lease.Job.ID)); err != nil {
		return domain.SubtitleCacheCleanupBatch{}, err
	}
	if len(sources) > 0 {
		rows, err := tx.Query(ctx, `SELECT id::text FROM media_sources WHERE id=ANY($1::uuid[])`, sources)
		if err != nil {
			return domain.SubtitleCacheCleanupBatch{}, storageError(err)
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return domain.SubtitleCacheCleanupBatch{}, storageError(err)
			}
			batch.Live[id] = true
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return domain.SubtitleCacheCleanupBatch{}, storageError(err)
		}
	}
	if total >= 0 {
		// The directory count when the first run listed the cache; an
		// estimate, like the admission totals of other kinds.
		// The fence above holds the job row.
		if _, err = tx.Exec(ctx, `UPDATE jobs SET progress_total=$2,progress_total_at=clock_timestamp() WHERE id=$1::uuid AND progress_total IS NULL`, lease.Job.ID, total); err != nil {
			return domain.SubtitleCacheCleanupBatch{}, storageError(err)
		}
	}
	return batch, storageError(tx.Commit(ctx))
}

// RecordSubtitleCacheSources implements
// app.SubtitleCacheCleanupExecutionRepository. cursor is the last source
// finished ("" when none); finished and failed count the directories since
// the previous checkpoint; last moves the job to the capacity phase.
func (s *Store) RecordSubtitleCacheSources(ctx context.Context, lease domain.JobLease, cursor string, finished, failed int, sweep domain.SubtitleCacheSweep, last bool) error {
	if ctx == nil || cursor != "" && !domain.ValidID(cursor) || finished < 0 || failed < 0 || finished+failed > 1000 || !domain.ValidSubtitleCacheSweep(sweep) || cursor == "" && finished+failed > 0 {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedKind(ctx, tx, lease, domain.JobSubtitleCacheCleanup)
	if err != nil {
		return err
	}
	phase := domain.SubtitleCachePhaseSources
	if last {
		phase = domain.SubtitleCachePhaseCapacity
	}
	var sources, failures int64
	err = tx.QueryRow(ctx, `UPDATE subtitle_cache_cleanup_job_state SET cursor_source=COALESCE($2::uuid,cursor_source),phase=$3,sources=sources+$4,failed=failed+$5,orphaned=orphaned+$6,revisions=revisions+$7,freed_bytes=freed_bytes+$8,updated_at=clock_timestamp()
 WHERE job_id=$1::uuid AND phase='sources' AND ($2::uuid IS NULL OR cursor_source IS NULL OR cursor_source<$2::uuid) RETURNING sources,failed`,
		lease.Job.ID, nullableID(cursor), phase, finished, failed, sweep.Orphaned, sweep.Revisions, sweep.Bytes).Scan(&sources, &failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrConflict
	}
	if err != nil {
		return storageError(err)
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET files=$2,skipped=$3,bytes=bytes+$4 WHERE id=$1::uuid`, lease.Job.ID, sources, failures, sweep.Bytes); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// RecordSubtitleCacheCapacity implements
// app.SubtitleCacheCleanupExecutionRepository: the size bound was enforced.
func (s *Store) RecordSubtitleCacheCapacity(ctx context.Context, lease domain.JobLease, sweep domain.SubtitleCacheSweep) error {
	if ctx == nil || !domain.ValidSubtitleCacheSweep(sweep) {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedKind(ctx, tx, lease, domain.JobSubtitleCacheCleanup)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE subtitle_cache_cleanup_job_state SET phase='done',capacity=capacity+$2,failed=failed+$3,freed_bytes=freed_bytes+$4,updated_at=clock_timestamp() WHERE job_id=$1::uuid AND phase='capacity'`,
		lease.Job.ID, sweep.Capacity, sweep.Failed, sweep.Bytes)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET bytes=bytes+$2 WHERE id=$1::uuid`, lease.Job.ID, sweep.Bytes); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// FinishSubtitleCacheCleanup makes a cleanup job terminal.
func (s *Store) FinishSubtitleCacheCleanup(ctx context.Context, l domain.JobLease, state, code string) error {
	return s.finishKindJob(ctx, l, domain.JobSubtitleCacheCleanup, "subtitle_cache_cleanup_failed", state, code, nil)
}

// --- backup_reminder ---

// lastMetadataExport is the time of the newest recorded metadata export, nil
// when none is recorded. Exports are security audit rows, kept for the
// security retention.
func lastMetadataExport(ctx context.Context, tx pgx.Tx) (*time.Time, time.Time, error) {
	var last *time.Time
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT (SELECT occurred_at FROM audit_logs WHERE category='security' AND event='metadata.exported' ORDER BY occurred_at DESC LIMIT 1),clock_timestamp()`).Scan(&last, &now)
	return last, now, storageError(err)
}

func insertBackupReminder(ctx context.Context, tx pgx.Tx, library string, actor *string, key, priority, trigger string, days int, policy domain.JobPolicy) (domain.Job, error) {
	one := int64(1)
	job, err := insertKindJob(ctx, tx, library, actor, key, domain.JobBackupReminder, priority, policy, &one)
	if err != nil {
		return domain.Job{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backup_reminder_job_state(job_id,trigger,threshold_days,updated_at) VALUES($1::uuid,$2,$3,clock_timestamp())`, job.ID, trigger, days); err != nil {
		return domain.Job{}, storageError(err)
	}
	return job, nil
}

// SubmitBackupReminderJob admits a backup check under the first idle
// library. Replay of the same key and priority returns the first job.
func (s *Store) SubmitBackupReminderJob(ctx context.Context, actor domain.Actor, key, priority string, days int, policy domain.JobPolicy) (domain.Job, bool, error) {
	if ctx == nil || !validJobKey(key) || !validJobPolicy(policy) || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) || !domain.ValidBackupReminderDays(days) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	if old, found, err := replayKindJob(ctx, tx, actor, key, domain.JobBackupReminder, "", priority); err != nil || found {
		if err != nil {
			return domain.Job{}, false, err
		}
		return old, true, storageError(tx.Commit(ctx))
	}
	library, err := anchorInstanceJob(ctx, tx, domain.JobBackupReminder, policy)
	if err != nil {
		return domain.Job{}, false, err
	}
	job, err := insertBackupReminder(ctx, tx, library, &actor.UserID, key, priority, domain.BackupReminderManual, days, policy)
	if err != nil {
		return domain.Job{}, false, err
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "backup_reminder.submitted", job.ID, nil, map[string]any{"anchorLibraryId": library, "priority": priority, "thresholdDays": days}); err != nil {
		return domain.Job{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, storageError(tx.Commit(ctx))
}

// DispatchBackupReminder queues a background backup check when none was
// queued within interval. A server without libraries, every library busy or
// a full queue leaves it for a later call.
func (s *Store) DispatchBackupReminder(ctx context.Context, days int, interval time.Duration, policy domain.JobPolicy) (bool, error) {
	if ctx == nil || !domain.ValidBackupReminderDays(days) || interval < time.Hour || !validJobPolicy(policy) {
		return false, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var recent bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE kind='backup_reminder' AND (state IN ('queued','running') OR created_at>clock_timestamp()-$1*interval '1 microsecond'))`, interval.Microseconds()).Scan(&recent); err != nil {
		return false, storageError(err)
	}
	if recent {
		return false, storageError(tx.Commit(ctx))
	}
	library, err := anchorInstanceJob(ctx, tx, domain.JobBackupReminder, policy)
	switch {
	case errors.Is(err, domain.ErrJobBusy), errors.Is(err, domain.ErrJobQueueFull), errors.Is(err, domain.ErrConflict):
		return false, storageError(tx.Commit(ctx))
	case err != nil:
		return false, err
	}
	var at time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return false, storageError(err)
	}
	if _, err = insertBackupReminder(ctx, tx, library, nil, "backup-reminder:"+at.UTC().Format(time.RFC3339Nano), domain.JobPriorityBackground, domain.BackupReminderSchedule, days, policy); err != nil {
		return false, err
	}
	return true, storageError(tx.Commit(ctx))
}

func scanBackupReminderState(row pgx.Row) (domain.BackupReminderJobState, error) {
	var v domain.BackupReminderJobState
	var status *string
	var last, checked *time.Time
	if err := row.Scan(&v.JobID, &v.Trigger, &v.ThresholdDays, &status, &last, &checked, &v.UpdatedAt); err != nil {
		return v, storageError(err)
	}
	v.UpdatedAt = v.UpdatedAt.UTC()
	if status != nil && checked != nil {
		result := domain.EvaluateBackupStatus(*checked, last, v.ThresholdDays)
		v.Result = &result
	}
	return v, nil
}

const backupReminderStateColumns = `job_id::text,trigger,threshold_days,status,last_export_at,checked_at,updated_at`

// GetBackupReminderState reads the result of a backup reminder job.
func (s *Store) GetBackupReminderState(ctx context.Context, actor domain.Actor, job string) (domain.BackupReminderJobState, error) {
	var v domain.BackupReminderJobState
	err := s.readState(ctx, actor, job, func(tx pgx.Tx) (err error) {
		v, err = scanBackupReminderState(tx.QueryRow(ctx, `SELECT `+backupReminderStateColumns+` FROM backup_reminder_job_state WHERE job_id=$1::uuid`, job))
		return err
	})
	return v, err
}

// BackupStatus evaluates the last metadata export against days now, for an
// administrator, without queueing anything.
func (s *Store) BackupStatus(ctx context.Context, actor domain.Actor, days int) (domain.BackupStatus, error) {
	if ctx == nil || !domain.ValidBackupReminderDays(days) {
		return domain.BackupStatus{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.BackupStatus{}, err
	}
	defer tx.Rollback(ctx)
	last, now, err := lastMetadataExport(ctx, tx)
	if err != nil {
		return domain.BackupStatus{}, err
	}
	return domain.EvaluateBackupStatus(now, last, days), storageError(tx.Commit(ctx))
}

// CheckBackupReminder implements app.BackupReminderExecutionRepository. The
// first run records the result with the database clock and, unless backups
// are on time, a backup.reminder audit entry; a resumed run returns the
// recorded result without a second entry.
func (s *Store) CheckBackupReminder(ctx context.Context, lease domain.JobLease) (domain.BackupStatus, error) {
	if ctx == nil {
		return domain.BackupStatus{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.BackupStatus{}, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedKind(ctx, tx, lease, domain.JobBackupReminder)
	if err != nil {
		return domain.BackupStatus{}, err
	}
	if current.Job.CancelRequested {
		return domain.BackupStatus{}, context.Canceled
	}
	state, err := scanBackupReminderState(tx.QueryRow(ctx, `SELECT `+backupReminderStateColumns+` FROM backup_reminder_job_state WHERE job_id=$1::uuid FOR UPDATE`, lease.Job.ID))
	if err != nil {
		return domain.BackupStatus{}, err
	}
	if state.Result != nil {
		return *state.Result, storageError(tx.Commit(ctx))
	}
	last, now, err := lastMetadataExport(ctx, tx)
	if err != nil {
		return domain.BackupStatus{}, err
	}
	result := domain.EvaluateBackupStatus(now, last, state.ThresholdDays)
	if _, err = tx.Exec(ctx, `UPDATE backup_reminder_job_state SET status=$2,last_export_at=$3,checked_at=$4,updated_at=clock_timestamp() WHERE job_id=$1::uuid`, lease.Job.ID, result.Status, result.LastExportAt, result.CheckedAt); err != nil {
		return domain.BackupStatus{}, storageError(err)
	}
	if result.Status != domain.BackupReminderOK {
		if err = auditAccount(ctx, tx, domain.Actor{}, "backup.reminder", lease.Job.ID, nil, map[string]any{"status": result.Status, "thresholdDays": result.ThresholdDays, "lastExportAt": result.LastExportAt, "ageSeconds": result.AgeSeconds, "trigger": state.Trigger}); err != nil {
			return domain.BackupStatus{}, err
		}
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET files=1 WHERE id=$1::uuid`, lease.Job.ID); err != nil {
		return domain.BackupStatus{}, err
	}
	return result, storageError(tx.Commit(ctx))
}

// FinishBackupReminder makes a backup reminder job terminal.
func (s *Store) FinishBackupReminder(ctx context.Context, l domain.JobLease, state, code string) error {
	return s.finishKindJob(ctx, l, domain.JobBackupReminder, "backup_reminder_failed", state, code, nil)
}

// --- external_metadata_removal ---

// externalRemovalCandidate selects the items of library $1 a removal visits:
// those with a provider-sourced field or an automatic match review that
// holds provider candidate text (a rejected one only when $2).
const externalRemovalCandidate = `i.library_id=$1::uuid AND (EXISTS(SELECT 1 FROM item_metadata_fields f WHERE f.item_id=i.id AND f.source='tmdb')
 OR EXISTS(SELECT 1 FROM metadata_refresh_reviews r WHERE r.item_id=i.id AND (r.state='pending' OR $2::boolean)))`

// SubmitExternalMetadataRemovalJob admits a removal of the external metadata
// of one library, which must have no active job. Its progress total is the
// candidate count at admission. Replay of the same key, library and priority
// returns the first job.
func (s *Store) SubmitExternalMetadataRemovalJob(ctx context.Context, actor domain.Actor, library, key string, input domain.ExternalMetadataRemovalInput, policy domain.JobPolicy) (domain.Job, bool, error) {
	if ctx == nil || !domain.ValidID(library) || !validJobKey(key) || !validJobPolicy(policy) || (input.Priority != domain.JobPriorityManual && input.Priority != domain.JobPriorityBackground) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if !input.Confirm {
		return domain.Job{}, false, domain.ErrConfirmationRequired
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	if old, found, err := replayKindJob(ctx, tx, actor, key, domain.JobExternalMetadataRemoval, library, input.Priority); err != nil || found {
		if err != nil {
			return domain.Job{}, false, err
		}
		return old, true, storageError(tx.Commit(ctx))
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT true FROM libraries WHERE id=$1::uuid FOR SHARE`, library).Scan(&exists); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if err = libraryAdmission(ctx, tx, library, policy); err != nil {
		return domain.Job{}, false, err
	}
	var total int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM items i WHERE `+externalRemovalCandidate, library, input.DropRejectedReviews).Scan(&total); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	job, err := insertKindJob(ctx, tx, library, &actor.UserID, key, domain.JobExternalMetadataRemoval, input.Priority, policy, &total)
	if err != nil {
		return domain.Job{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO external_metadata_removal_job_state(job_id,library_id,drop_rejected_reviews,updated_at) VALUES($1::uuid,$2::uuid,$3,clock_timestamp())`, job.ID, library, input.DropRejectedReviews); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "external_metadata_removal.submitted", job.ID, nil, map[string]any{"libraryId": library, "priority": input.Priority, "confirmed": true, "dropRejectedReviews": input.DropRejectedReviews, "candidates": total}); err != nil {
		return domain.Job{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, storageError(tx.Commit(ctx))
}

const externalRemovalStateColumns = `job_id::text,library_id::text,drop_rejected_reviews,cursor_item_id::text,removed,fields,kept,skipped,failed,reviews,updated_at`

func scanExternalRemovalState(row pgx.Row) (domain.ExternalMetadataRemovalJobState, error) {
	var v domain.ExternalMetadataRemovalJobState
	if err := row.Scan(&v.JobID, &v.LibraryID, &v.DropRejectedReviews, &v.CursorItemID, &v.Removed, &v.Fields, &v.Kept, &v.Skipped, &v.Failed, &v.Reviews, &v.UpdatedAt); err != nil {
		return v, storageError(err)
	}
	v.UpdatedAt = v.UpdatedAt.UTC()
	return v, nil
}

// GetExternalMetadataRemovalState reads the progress of a removal job.
func (s *Store) GetExternalMetadataRemovalState(ctx context.Context, actor domain.Actor, job string) (domain.ExternalMetadataRemovalJobState, error) {
	var v domain.ExternalMetadataRemovalJobState
	err := s.readState(ctx, actor, job, func(tx pgx.Tx) (err error) {
		v, err = scanExternalRemovalState(tx.QueryRow(ctx, `SELECT `+externalRemovalStateColumns+` FROM external_metadata_removal_job_state WHERE job_id=$1::uuid`, job))
		return err
	})
	return v, err
}

// jobAdministrator is the job's actor while they are an active
// administrator; anyone else is domain.ErrForbidden. Removal runs as its
// submitter and stops once they lose the right.
func jobAdministrator(ctx context.Context, tx pgx.Tx, job string) (string, error) {
	var actor string
	err := tx.QueryRow(ctx, `SELECT u.id::text FROM jobs j JOIN users u ON u.id=j.actor_id WHERE j.id=$1::uuid AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL`, job).Scan(&actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrForbidden
	}
	return actor, storageError(err)
}

// NextExternalMetadataRemovalBatch implements
// app.ExternalMetadataRemovalExecutionRepository: up to limit candidate
// items after the cursor in ascending ID order. It answers context.Canceled
// once cancellation was requested and domain.ErrForbidden once the submitter
// is no longer an active administrator.
func (s *Store) NextExternalMetadataRemovalBatch(ctx context.Context, lease domain.JobLease, limit int) ([]string, error) {
	if ctx == nil || limit < 1 || limit > 1000 {
		return nil, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedKind(ctx, tx, lease, domain.JobExternalMetadataRemoval)
	if err != nil {
		return nil, err
	}
	if current.Job.CancelRequested {
		return nil, context.Canceled
	}
	if _, err = jobAdministrator(ctx, tx, lease.Job.ID); err != nil {
		return nil, err
	}
	state, err := scanExternalRemovalState(tx.QueryRow(ctx, `SELECT `+externalRemovalStateColumns+` FROM external_metadata_removal_job_state WHERE job_id=$1::uuid`, lease.Job.ID))
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT i.id::text FROM items i WHERE `+externalRemovalCandidate+` AND ($3::uuid IS NULL OR i.id>$3::uuid) ORDER BY i.id LIMIT $4`, state.LibraryID, state.DropRejectedReviews, state.CursorItemID, limit)
	if err != nil {
		return nil, storageError(err)
	}
	items := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		items = append(items, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return items, storageError(tx.Commit(ctx))
}

// recordExternalRemoval moves the cursor to item and adds one outcome; the
// job's done unit is every finished item.
func recordExternalRemoval(ctx context.Context, tx pgx.Tx, lease domain.JobLease, item string, o domain.ExternalMetadataRemovalOutcome, failed bool) error {
	removed, kept, skipped, failures := 0, 0, 0, 0
	switch {
	case failed:
		failures = 1
	case o.Removed:
		removed = 1
	case o.Kept:
		kept = 1
	default:
		skipped = 1
	}
	var done, other int64
	err := tx.QueryRow(ctx, `UPDATE external_metadata_removal_job_state SET cursor_item_id=$2::uuid,removed=removed+$3,kept=kept+$4,skipped=skipped+$5,failed=failed+$6,fields=fields+$7,reviews=reviews+$8,updated_at=clock_timestamp()
 WHERE job_id=$1::uuid AND (cursor_item_id IS NULL OR cursor_item_id<$2::uuid) RETURNING removed+kept,skipped+failed`,
		lease.Job.ID, item, removed, kept, skipped, failures, o.Fields, o.Reviews).Scan(&done, &other)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrConflict
	}
	if err != nil {
		return storageError(err)
	}
	return guardedJobUpdate(ctx, tx, lease, `UPDATE jobs SET files=$2,skipped=$3 WHERE id=$1::uuid`, lease.Job.ID, done, other)
}

// RemoveExternalMetadataForJob implements
// app.ExternalMetadataRemovalExecutionRepository. In one transaction under
// the lease fence it removes the item's unlocked provider fields with the
// rules of RemoveExternalMetadata (the revision advances and an audit entry
// naming the job is written only when a field was removed), deletes the
// item's pending automatic match review (and a rejected one when the job
// asks for it), and checkpoints the item. An item deleted meanwhile or
// moved to another library is skipped.
func (s *Store) RemoveExternalMetadataForJob(ctx context.Context, lease domain.JobLease, item string) (domain.ExternalMetadataRemovalOutcome, error) {
	if ctx == nil || !domain.ValidID(item) {
		return domain.ExternalMetadataRemovalOutcome{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.ExternalMetadataRemovalOutcome{}, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedKind(ctx, tx, lease, domain.JobExternalMetadataRemoval)
	if err != nil {
		return domain.ExternalMetadataRemovalOutcome{}, err
	}
	actor, err := jobAdministrator(ctx, tx, lease.Job.ID)
	if err != nil {
		return domain.ExternalMetadataRemovalOutcome{}, err
	}
	state, err := scanExternalRemovalState(tx.QueryRow(ctx, `SELECT `+externalRemovalStateColumns+` FROM external_metadata_removal_job_state WHERE job_id=$1::uuid`, lease.Job.ID))
	if err != nil {
		return domain.ExternalMetadataRemovalOutcome{}, err
	}
	var o domain.ExternalMetadataRemovalOutcome
	before, err := readItemMetadata(ctx, tx, item, true)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		o.Skipped = true
	case err != nil:
		return domain.ExternalMetadataRemovalOutcome{}, err
	case before.LibraryID != state.LibraryID:
		o.Skipped = true
	default:
		result, origins, err := removeExternalFields(ctx, tx, item, before)
		if err != nil {
			return domain.ExternalMetadataRemovalOutcome{}, err
		}
		o.Fields = len(result.Removed)
		switch {
		case len(result.Removed) > 0:
			o.Removed = true
			if _, err = tx.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, item, before.Revision+1); err != nil {
				return domain.ExternalMetadataRemovalOutcome{}, storageError(err)
			}
			if result.Metadata, err = readItemMetadata(ctx, tx, item, false); err != nil {
				return domain.ExternalMetadataRemovalOutcome{}, err
			}
			auditBefore := map[string]any{"revision": before.Revision, "provider": domain.ExternalMetadataSource, "removedOrigins": origins}
			auditAfter := map[string]any{"metadata": result.Metadata, "removed": result.Removed, "skipped": result.Skipped, "jobId": lease.Job.ID}
			if err = auditAccount(ctx, tx, domain.Actor{UserID: actor}, "item.external_metadata_removed", item, auditBefore, auditAfter); err != nil {
				return domain.ExternalMetadataRemovalOutcome{}, err
			}
		case len(result.Skipped) > 0:
			o.Kept = true
		default:
			o.Skipped = true
		}
		tag, err := tx.Exec(ctx, `DELETE FROM metadata_refresh_reviews WHERE item_id=$1::uuid AND (state='pending' OR $2::boolean)`, item, state.DropRejectedReviews)
		if err != nil {
			return domain.ExternalMetadataRemovalOutcome{}, storageError(err)
		}
		o.Reviews = int(tag.RowsAffected())
	}
	if err = recordExternalRemoval(ctx, tx, current, item, o, false); err != nil {
		return domain.ExternalMetadataRemovalOutcome{}, err
	}
	return o, storageError(tx.Commit(ctx))
}

// RecordExternalMetadataRemovalFailure implements
// app.ExternalMetadataRemovalExecutionRepository: the item could not be
// changed; it is counted as failed and the cursor moves past it.
func (s *Store) RecordExternalMetadataRemovalFailure(ctx context.Context, lease domain.JobLease, item string) error {
	if ctx == nil || !domain.ValidID(item) {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedKind(ctx, tx, lease, domain.JobExternalMetadataRemoval)
	if err != nil {
		return err
	}
	if err = recordExternalRemoval(ctx, tx, current, item, domain.ExternalMetadataRemovalOutcome{}, true); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// FinishExternalMetadataRemoval makes a removal job terminal and audits its
// counters with the outcome.
func (s *Store) FinishExternalMetadataRemoval(ctx context.Context, l domain.JobLease, state, code string) error {
	return s.finishKindJob(ctx, l, domain.JobExternalMetadataRemoval, "external_metadata_removal_failed", state, code, func(tx pgx.Tx, _ domain.JobLease) error {
		v, err := scanExternalRemovalState(tx.QueryRow(ctx, `SELECT `+externalRemovalStateColumns+` FROM external_metadata_removal_job_state WHERE job_id=$1::uuid`, l.Job.ID))
		if err != nil {
			return err
		}
		var actor *string
		if err = tx.QueryRow(ctx, `SELECT actor_id::text FROM jobs WHERE id=$1::uuid`, l.Job.ID).Scan(&actor); err != nil {
			return storageError(err)
		}
		who := domain.Actor{}
		if actor != nil {
			who.UserID = *actor
		}
		return auditAccount(ctx, tx, who, "external_metadata_removal.finished", l.Job.ID, nil, map[string]any{"libraryId": v.LibraryID, "state": state, "errorCode": code,
			"removed": v.Removed, "fields": v.Fields, "kept": v.Kept, "skipped": v.Skipped, "failed": v.Failed, "reviews": v.Reviews})
	})
}
