package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
	"github.com/jackc/pgx/v5"
)

var (
	_ app.NFOApplyJobRepository       = (*Store)(nil)
	_ app.NFOApplyExecutionRepository = (*Store)(nil)
)

// SubmitNFOApplyJob admits a batch NFO apply of one library. The library must
// be read-only (domain.ErrConflict otherwise) and have no active job. Replay
// of the same key, library and priority returns the first job.
func (s *Store) SubmitNFOApplyJob(ctx context.Context, actor domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error) {
	if ctx == nil || !domain.ValidID(library) || !validJobKey(key) || !validJobPolicy(policy) || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	old, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE actor_id=$1 AND idempotency_key=$2`, actor.UserID, key))
	if err == nil {
		if old.Kind != domain.JobNFOApply || old.LibraryID != library || old.Priority != priority {
			return domain.Job{}, false, domain.ErrConflict
		}
		if err = probeAdminStillLive(ctx, tx, actor); err != nil {
			return domain.Job{}, false, err
		}
		return old, true, storageError(tx.Commit(ctx))
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Job{}, false, err
	}
	var readOnly bool
	if err = tx.QueryRow(ctx, `SELECT nfo_mode='read-only' FROM libraries WHERE id=$1::uuid FOR SHARE`, library).Scan(&readOnly); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if !readOnly {
		return domain.Job{}, false, domain.ErrConflict
	}
	if err = libraryAdmission(ctx, tx, library, policy); err != nil {
		return domain.Job{}, false, err
	}
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,$2::uuid,$3,'nfo_apply',$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+jobColumns,
		library, actor.UserID, key, priority, policy.QueueLimit, policy.HistoryLimit, policy.MaxEntries, policy.MaxDirectories, policy.MaxAttempts, policy.MissingCountLimit, policy.MissingPercentLimit))
	if err != nil {
		return domain.Job{}, false, err
	}
	tracing.Remember(ctx, tracing.LinkJob, job.ID)
	if _, err = tx.Exec(ctx, `INSERT INTO nfo_apply_job_state(job_id,library_id,updated_at) VALUES($1::uuid,$2::uuid,clock_timestamp())`, job.ID, library); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "nfo.apply_submitted", job.ID, nil, map[string]any{"libraryId": library, "priority": priority}); err != nil {
		return domain.Job{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, storageError(tx.Commit(ctx))
}

// GetNFOApplyState reads the progress of an nfo_apply job. A job of another
// kind has no state row and is domain.ErrNotFound.
func (s *Store) GetNFOApplyState(ctx context.Context, actor domain.Actor, job string) (domain.NFOApplyJobState, error) {
	if ctx == nil {
		return domain.NFOApplyJobState{}, domain.ErrInvalid
	}
	if !domain.ValidID(job) {
		return domain.NFOApplyJobState{}, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.NFOApplyJobState{}, err
	}
	defer tx.Rollback(ctx)
	var v domain.NFOApplyJobState
	err = tx.QueryRow(ctx, `SELECT job_id::text,library_id::text,cursor_item_id::text,applied,unchanged,skipped,failed,updated_at FROM nfo_apply_job_state WHERE job_id=$1::uuid`, job).
		Scan(&v.JobID, &v.LibraryID, &v.CursorItemID, &v.Applied, &v.Unchanged, &v.Skipped, &v.Failed, &v.UpdatedAt)
	if err != nil {
		return domain.NFOApplyJobState{}, storageError(err)
	}
	v.UpdatedAt = v.UpdatedAt.UTC()
	return v, storageError(tx.Commit(ctx))
}

// fencedNFOApply fences the lease and requires an nfo_apply job.
func fencedNFOApply(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, error) {
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.JobLease{}, err
	}
	if current.Job.Kind != domain.JobNFOApply {
		return domain.JobLease{}, domain.ErrInvalid
	}
	return current, nil
}

// NextNFOApplyBatch implements app.NFOApplyExecutionRepository. The actor is
// the administrator who submitted the job, bound to their longest-lived live
// session, so a job outlives the session that submitted it but not the
// administrator's rights.
func (s *Store) NextNFOApplyBatch(ctx context.Context, lease domain.JobLease, limit int) (domain.NFOApplyBatch, error) {
	if ctx == nil || limit < 1 || limit > 1000 {
		return domain.NFOApplyBatch{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.NFOApplyBatch{}, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedNFOApply(ctx, tx, lease)
	if err != nil {
		return domain.NFOApplyBatch{}, err
	}
	if current.Job.CancelRequested {
		return domain.NFOApplyBatch{}, context.Canceled
	}
	var library string
	var cursor *string
	if err = tx.QueryRow(ctx, `SELECT library_id::text,cursor_item_id::text FROM nfo_apply_job_state WHERE job_id=$1::uuid`, lease.Job.ID).Scan(&library, &cursor); err != nil {
		return domain.NFOApplyBatch{}, storageError(err)
	}
	batch := domain.NFOApplyBatch{Actor: domain.Actor{}}
	err = tx.QueryRow(ctx, `SELECT u.id::text,s.id::text FROM jobs j JOIN users u ON u.id=j.actor_id JOIN sessions s ON s.user_id=u.id
 WHERE j.id=$1::uuid AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL AND s.revoked_at IS NULL AND s.expires_at>now()
 ORDER BY s.expires_at DESC,s.id LIMIT 1`, lease.Job.ID).Scan(&batch.Actor.UserID, &batch.Actor.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NFOApplyBatch{}, domain.ErrForbidden
	}
	if err != nil {
		return domain.NFOApplyBatch{}, storageError(err)
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM items WHERE library_id=$1::uuid AND ($2::uuid IS NULL OR id>$2::uuid) ORDER BY id LIMIT $3`, library, cursor, limit)
	if err != nil {
		return domain.NFOApplyBatch{}, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return domain.NFOApplyBatch{}, storageError(err)
		}
		batch.Items = append(batch.Items, id)
	}
	if err = rows.Err(); err != nil {
		return domain.NFOApplyBatch{}, storageError(err)
	}
	rows.Close()
	return batch, storageError(tx.Commit(ctx))
}

// RecordNFOApplyBatch implements app.NFOApplyExecutionRepository. The cursor
// only moves forward; the counters and the job's progress columns change in
// the same fenced transaction.
func (s *Store) RecordNFOApplyBatch(ctx context.Context, lease domain.JobLease, cursor string, c domain.NFOApplyCounts) error {
	if ctx == nil || !domain.ValidID(cursor) || c.Applied < 0 || c.Unchanged < 0 || c.Skipped < 0 || c.Failed < 0 {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = fencedNFOApply(ctx, tx, lease); err != nil {
		return err
	}
	var applied, unchanged, skipped, failed int64
	err = tx.QueryRow(ctx, `UPDATE nfo_apply_job_state SET cursor_item_id=$2::uuid,applied=applied+$3,unchanged=unchanged+$4,skipped=skipped+$5,failed=failed+$6,updated_at=clock_timestamp()
 WHERE job_id=$1::uuid AND (cursor_item_id IS NULL OR cursor_item_id<$2::uuid) RETURNING applied,unchanged,skipped,failed`, lease.Job.ID, cursor, c.Applied, c.Unchanged, c.Skipped, c.Failed).Scan(&applied, &unchanged, &skipped, &failed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrConflict
		}
		return storageError(err)
	}
	if err = guardedJobUpdate(ctx, tx, lease, `UPDATE jobs SET files=$2,skipped=$3 WHERE id=$1::uuid`, lease.Job.ID, applied+unchanged, skipped+failed); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// FinishNFOApply makes an nfo_apply job terminal under its lease fence.
func (s *Store) FinishNFOApply(ctx context.Context, l domain.JobLease, state, code string) error {
	if !validJobError(state, code) && !(state == domain.JobFailed && code == "nfo_apply_failed") {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedNFOApply(ctx, tx, l)
	if err != nil {
		return err
	}
	if state == domain.JobSucceeded && current.Job.CancelRequested {
		return domain.ErrConflict
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET state=$2,error_code=$3,finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE id=$1::uuid`, l.Job.ID, state, code); err != nil {
		return err
	}
	if err = trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
