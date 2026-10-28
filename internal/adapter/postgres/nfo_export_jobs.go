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
	_ app.NFOExportJobRepository       = (*Store)(nil)
	_ app.NFOExportExecutionRepository = (*Store)(nil)
)

// SubmitNFOExportJob admits a batch NFO export of one library. The library
// must be read-only with the write permission (domain.ErrNFOWriteDisabled
// otherwise) and have no active job. Replay of the same key, library and
// priority returns the first job.
func (s *Store) SubmitNFOExportJob(ctx context.Context, actor domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error) {
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
		if old.Kind != domain.JobNFOExport || old.LibraryID != library || old.Priority != priority {
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
	var writable bool
	if err = tx.QueryRow(ctx, `SELECT nfo_mode='read-only' AND nfo_write_enabled FROM libraries WHERE id=$1::uuid FOR SHARE`, library).Scan(&writable); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if !writable {
		return domain.Job{}, false, domain.ErrNFOWriteDisabled
	}
	if err = libraryAdmission(ctx, tx, library, policy); err != nil {
		return domain.Job{}, false, err
	}
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,$2::uuid,$3,'nfo_export',$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+jobColumns,
		library, actor.UserID, key, priority, policy.QueueLimit, policy.HistoryLimit, policy.MaxEntries, policy.MaxDirectories, policy.MaxAttempts, policy.MissingCountLimit, policy.MissingPercentLimit))
	if err != nil {
		return domain.Job{}, false, err
	}
	tracing.Remember(ctx, tracing.LinkJob, job.ID)
	if _, err = tx.Exec(ctx, `INSERT INTO nfo_export_job_state(job_id,library_id,updated_at) VALUES($1::uuid,$2::uuid,clock_timestamp())`, job.ID, library); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "nfo.export_submitted", job.ID, nil, map[string]any{"libraryId": library, "priority": priority}); err != nil {
		return domain.Job{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, storageError(tx.Commit(ctx))
}

// GetNFOExportState reads the progress of an nfo_export job. A job of another
// kind has no state row and is domain.ErrNotFound.
func (s *Store) GetNFOExportState(ctx context.Context, actor domain.Actor, job string) (domain.NFOExportJobState, error) {
	if ctx == nil {
		return domain.NFOExportJobState{}, domain.ErrInvalid
	}
	if !domain.ValidID(job) {
		return domain.NFOExportJobState{}, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.NFOExportJobState{}, err
	}
	defer tx.Rollback(ctx)
	var v domain.NFOExportJobState
	err = tx.QueryRow(ctx, `SELECT job_id::text,library_id::text,cursor_item_id::text,child_job_id::text,chunk,written,unchanged,skipped_missing,skipped_invalid,skipped_other,failed,updated_at FROM nfo_export_job_state WHERE job_id=$1::uuid`, job).
		Scan(&v.JobID, &v.LibraryID, &v.CursorItemID, &v.ChildJobID, &v.Chunks, &v.Written, &v.Unchanged, &v.SkippedMissing, &v.SkippedInvalid, &v.SkippedOther, &v.Failed, &v.UpdatedAt)
	if err != nil {
		return domain.NFOExportJobState{}, storageError(err)
	}
	v.UpdatedAt = v.UpdatedAt.UTC()
	return v, storageError(tx.Commit(ctx))
}

// fencedNFOExport fences the lease and requires an nfo_export job.
func fencedNFOExport(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, error) {
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.JobLease{}, err
	}
	if current.Job.Kind != domain.JobNFOExport {
		return domain.JobLease{}, domain.ErrInvalid
	}
	return current, nil
}

// nfoExportRow is the locked state row of an export job.
type nfoExportRow struct {
	library string
	cursor  *string
	chunk   int
	child   *string
	entries int
}

func lockNFOExportRow(ctx context.Context, tx pgx.Tx, job string) (nfoExportRow, error) {
	var r nfoExportRow
	err := tx.QueryRow(ctx, `SELECT library_id::text,cursor_item_id::text,chunk,child_job_id::text,child_entries FROM nfo_export_job_state WHERE job_id=$1::uuid FOR UPDATE`, job).
		Scan(&r.library, &r.cursor, &r.chunk, &r.child, &r.entries)
	return r, storageError(err)
}

// nfoExportProgress mirrors the counters into the job's files and skipped
// columns, under the lease fence: files are items exported or already equal,
// skipped are items left out or failed.
func nfoExportProgress(ctx context.Context, tx pgx.Tx, lease domain.JobLease) error {
	return guardedJobUpdate(ctx, tx, lease, `UPDATE jobs SET files=(SELECT written+unchanged FROM nfo_export_job_state WHERE job_id=$1::uuid),skipped=(SELECT skipped_missing+skipped_invalid+skipped_other+failed FROM nfo_export_job_state WHERE job_id=$1::uuid) WHERE id=$1::uuid`, lease.Job.ID)
}

// requestNFOExportChildCancel asks the export job's outstanding child to stop,
// as CancelJob does for any nfo_write job: a queued child ends at once, a
// running one at its next heartbeat. A terminal child is left alone.
func requestNFOExportChildCancel(ctx context.Context, tx pgx.Tx, child string) error {
	_, err := tx.Exec(ctx, `UPDATE jobs SET cancel_requested=true,state=CASE WHEN state='queued' THEN 'cancelled' ELSE state END,finished_at=CASE WHEN state='queued' THEN clock_timestamp() ELSE finished_at END WHERE id=$1::uuid AND state IN ('queued','running')`, child)
	return storageError(err)
}

// settleNFOExportChild folds a terminal child into the counters: entries the
// child replaced are written, every other prepared entry is failed. A child
// that was trimmed from the history counts as failed. It reports false and
// changes nothing while the child is queued or running.
func settleNFOExportChild(ctx context.Context, tx pgx.Tx, job string, row nfoExportRow) (bool, error) {
	var state string
	err := tx.QueryRow(ctx, `SELECT state FROM jobs WHERE id=$1::uuid FOR UPDATE`, *row.child).Scan(&state)
	missing := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !missing {
		return false, storageError(err)
	}
	if !missing && (state == domain.JobQueued || state == domain.JobRunning) {
		return false, nil
	}
	written := 0
	if !missing {
		// A settlement phase of 2 is the terminal "replaced" of a journal token.
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM nfo_write_entries e WHERE e.job_id=$1::uuid AND EXISTS(SELECT 1 FROM nfo_write_commit_journal w JOIN nfo_write_commit_settlements s ON s.token=w.token WHERE w.job_id=e.job_id AND w.sequence=e.sequence AND s.phase=$2)`, *row.child, int16(domain.NFOWriteCommitReplaced)).Scan(&written); err != nil {
			return false, storageError(err)
		}
	}
	if written > row.entries {
		return false, domain.ErrDatabase
	}
	_, err = tx.Exec(ctx, `UPDATE nfo_export_job_state SET child_job_id=NULL,child_entries=0,written=written+$2,failed=failed+$3,updated_at=clock_timestamp() WHERE job_id=$1::uuid`, job, written, row.entries-written)
	return err == nil, storageError(err)
}

// NextNFOExportStep implements app.NFOExportExecutionRepository. A finished
// child is folded into the counters first. While a child is outstanding the
// step only says Waiting. The actor is the administrator who submitted the job,
// bound to their longest-lived live session, so a job outlives the session
// that submitted it but not the administrator's rights.
func (s *Store) NextNFOExportStep(ctx context.Context, lease domain.JobLease, limit int) (domain.NFOExportStep, error) {
	if ctx == nil || limit < 1 || limit > 1000 {
		return domain.NFOExportStep{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.NFOExportStep{}, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedNFOExport(ctx, tx, lease)
	if err != nil {
		return domain.NFOExportStep{}, err
	}
	row, err := lockNFOExportRow(ctx, tx, lease.Job.ID)
	if err != nil {
		return domain.NFOExportStep{}, err
	}
	if row.child != nil {
		if current.Job.CancelRequested {
			if err = requestNFOExportChildCancel(ctx, tx, *row.child); err != nil {
				return domain.NFOExportStep{}, err
			}
		}
		settled, err := settleNFOExportChild(ctx, tx, lease.Job.ID, row)
		if err != nil {
			return domain.NFOExportStep{}, err
		}
		if !settled {
			return domain.NFOExportStep{Waiting: true}, storageError(tx.Commit(ctx))
		}
		if err = nfoExportProgress(ctx, tx, lease); err != nil {
			return domain.NFOExportStep{}, err
		}
	}
	if current.Job.CancelRequested {
		// The settled child stays recorded; the caller stops.
		if err = tx.Commit(ctx); err != nil {
			return domain.NFOExportStep{}, storageError(err)
		}
		return domain.NFOExportStep{}, context.Canceled
	}
	step := domain.NFOExportStep{Chunk: row.chunk}
	err = tx.QueryRow(ctx, `SELECT u.id::text,s.id::text FROM jobs j JOIN users u ON u.id=j.actor_id JOIN sessions s ON s.user_id=u.id
 WHERE j.id=$1::uuid AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL AND s.revoked_at IS NULL AND s.expires_at>now()
 ORDER BY s.expires_at DESC,s.id LIMIT 1`, lease.Job.ID).Scan(&step.Actor.UserID, &step.Actor.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NFOExportStep{}, domain.ErrForbidden
	}
	if err != nil {
		return domain.NFOExportStep{}, storageError(err)
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM items WHERE library_id=$1::uuid AND ($2::uuid IS NULL OR id>$2::uuid) ORDER BY id LIMIT $3`, row.library, row.cursor, limit)
	if err != nil {
		return domain.NFOExportStep{}, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return domain.NFOExportStep{}, storageError(err)
		}
		step.Items = append(step.Items, id)
	}
	if err = rows.Err(); err != nil {
		return domain.NFOExportStep{}, storageError(err)
	}
	rows.Close()
	return step, storageError(tx.Commit(ctx))
}

// RecordNFOExportBatch implements app.NFOExportExecutionRepository for items
// that need no child job. The cursor only moves forward; the counters and the
// job's progress columns change in the same fenced transaction.
func (s *Store) RecordNFOExportBatch(ctx context.Context, lease domain.JobLease, cursor string, c domain.NFOExportCounts) error {
	if ctx == nil || !domain.ValidID(cursor) || !c.Valid() || c.Written != 0 {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = fencedNFOExport(ctx, tx, lease); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE nfo_export_job_state SET cursor_item_id=$2::uuid,unchanged=unchanged+$3,skipped_missing=skipped_missing+$4,skipped_invalid=skipped_invalid+$5,skipped_other=skipped_other+$6,failed=failed+$7,updated_at=clock_timestamp()
 WHERE job_id=$1::uuid AND child_job_id IS NULL AND (cursor_item_id IS NULL OR cursor_item_id<$2::uuid)`, lease.Job.ID, cursor, c.Unchanged, c.SkippedMissing, c.SkippedInvalid, c.SkippedOther, c.Failed)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	if err = nfoExportProgress(ctx, tx, lease); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// SubmitNFOExportChunk implements app.NFOExportExecutionRepository. In one
// transaction under the export lease it creates the child nfo_write job from
// the chunk's preparations, discards those preparations (the child owns its
// copies, and the per-actor preparation limit stays free), and records the
// child, the chunk number, the cursor and the counters. The child belongs to
// the export job (export_job_id) and so may be active beside it. A crash leaves
// either all of this or none of it.
func (s *Store) SubmitNFOExportChunk(ctx context.Context, lease domain.JobLease, actor domain.Actor, chunk int, cursor string, c domain.NFOExportCounts, preparations []string) error {
	if ctx == nil || chunk < 1 || !domain.ValidID(cursor) || !c.Valid() || c.Written != 0 || len(preparations) < 1 || len(preparations) > domain.NFOExportChunkSize {
		return domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedNFOExport(ctx, tx, lease)
	if err != nil {
		return err
	}
	if current.Job.CancelRequested {
		return context.Canceled
	}
	row, err := lockNFOExportRow(ctx, tx, lease.Job.ID)
	if err != nil {
		return err
	}
	if row.child != nil || row.chunk != chunk-1 || (row.cursor != nil && *row.cursor >= cursor) {
		return domain.ErrConflict
	}
	var writable bool
	var jobActor, priority string
	if err = tx.QueryRow(ctx, `SELECT l.nfo_mode='read-only' AND l.nfo_write_enabled,j.actor_id::text,j.priority FROM jobs j JOIN libraries l ON l.id=j.library_id WHERE j.id=$1::uuid FOR SHARE OF l`, lease.Job.ID).Scan(&writable, &jobActor, &priority); err != nil {
		return storageError(err)
	}
	if jobActor != actor.UserID {
		return domain.ErrForbidden
	}
	if !writable {
		return domain.ErrNFOWriteDisabled
	}
	// The child takes the export job's policy; the queue limit was applied when
	// the export job was admitted.
	child, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,export_job_id,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit)
 SELECT library_id,actor_id,$2,'nfo_write',priority,id,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit FROM jobs WHERE id=$1::uuid RETURNING `+jobColumns, lease.Job.ID, domain.NFOExportChildKey(lease.Job.ID, chunk)))
	if err != nil {
		return err
	}
	tracing.Remember(ctx, tracing.LinkJob, child.ID)
	if err = copyNFOWriteIntents(ctx, tx, child.ID, preparations); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			err = domain.ErrConflict
		}
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM nfo_write_preparations WHERE actor_id=$1::uuid AND starts_with(idempotency_key,$2)`, actor.UserID, domain.NFOExportPreparationKeyPrefix(lease.Job.ID, chunk)); err != nil {
		return storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE nfo_export_job_state SET cursor_item_id=$2::uuid,chunk=$3,child_job_id=$4::uuid,child_entries=$5,unchanged=unchanged+$6,skipped_missing=skipped_missing+$7,skipped_invalid=skipped_invalid+$8,skipped_other=skipped_other+$9,failed=failed+$10,updated_at=clock_timestamp() WHERE job_id=$1::uuid`,
		lease.Job.ID, cursor, chunk, child.ID, len(preparations), c.Unchanged, c.SkippedMissing, c.SkippedInvalid, c.SkippedOther, c.Failed); err != nil {
		return storageError(err)
	}
	if err = nfoExportProgress(ctx, tx, lease); err != nil {
		return err
	}
	if err = trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, actor, "nfo.write_submitted", child.ID, nil, map[string]any{"total": len(preparations), "exportJobId": lease.Job.ID, "chunk": chunk}); err != nil {
		return err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// YieldNFOExport implements app.NFOExportExecutionRepository. It hands the
// worker back by returning the job to the queue without consuming an attempt;
// the claim gate keeps the job there until its child is terminal. The lease
// fence stops a stale owner from requeueing a job another owner holds.
func (s *Store) YieldNFOExport(ctx context.Context, lease domain.JobLease) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = fencedNFOExport(ctx, tx, lease); err != nil {
		return err
	}
	if err = requeueNFOExport(ctx, tx, lease); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

func requeueNFOExport(ctx context.Context, tx pgx.Tx, lease domain.JobLease) error {
	return guardedJobUpdate(ctx, tx, lease, `UPDATE jobs SET state='queued',attempts=GREATEST(attempts-1,0),owner=NULL,lease_until=NULL WHERE id=$1::uuid`, lease.Job.ID)
}

// FinishNFOExport makes an nfo_export job terminal under its lease fence. A
// job that stops with an outstanding child asks the child to stop; a cancelled
// job whose child is still running goes back to the queue instead (cancel
// requested, so it ends at its next turn), because the child's outcome is
// still to be counted. Succeeding with an unsettled child is a conflict.
func (s *Store) FinishNFOExport(ctx context.Context, l domain.JobLease, state, code string) error {
	if !validJobError(state, code) && !(state == domain.JobFailed && code == "nfo_export_failed") {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedNFOExport(ctx, tx, l)
	if err != nil {
		return err
	}
	if state == domain.JobSucceeded && current.Job.CancelRequested {
		return domain.ErrConflict
	}
	row, err := lockNFOExportRow(ctx, tx, l.Job.ID)
	if err != nil {
		return err
	}
	if row.child != nil {
		if state == domain.JobSucceeded {
			return domain.ErrConflict
		}
		if err = requestNFOExportChildCancel(ctx, tx, *row.child); err != nil {
			return err
		}
		settled, err := settleNFOExportChild(ctx, tx, l.Job.ID, row)
		if err != nil {
			return err
		}
		if settled {
			if err = nfoExportProgress(ctx, tx, l); err != nil {
				return err
			}
		} else if state == domain.JobCancelled {
			if err = requeueNFOExport(ctx, tx, l); err != nil {
				return err
			}
			return storageError(tx.Commit(ctx))
		}
	}
	// Preparations of a chunk that never reached a child are of no use.
	if _, err = tx.Exec(ctx, `DELETE FROM nfo_write_preparations WHERE actor_id=(SELECT actor_id FROM jobs WHERE id=$1::uuid) AND starts_with(idempotency_key,$2)`, l.Job.ID, "nfo-export:"+l.Job.ID+":"); err != nil {
		return storageError(err)
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET state=$2,error_code=$3,finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE id=$1::uuid`, l.Job.ID, state, code); err != nil {
		return err
	}
	if err = trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// cancelNFOExportJob is CancelJob for an export job. A job without a child
// ends at once like any queued job. A job with a child keeps its place in the
// queue, because the child's outcome still has to be counted, and asks the
// child to stop; a worker finishes it as cancelled once the child is terminal.
func cancelNFOExportJob(ctx context.Context, tx pgx.Tx, id string) (domain.Job, error) {
	j, err := scanJob(tx.QueryRow(ctx, `UPDATE jobs SET cancel_requested=true,state=CASE WHEN state='queued' AND NOT EXISTS(SELECT 1 FROM nfo_export_job_state e WHERE e.job_id=jobs.id AND e.child_job_id IS NOT NULL) THEN 'cancelled' ELSE state END,
 finished_at=CASE WHEN state='queued' AND NOT EXISTS(SELECT 1 FROM nfo_export_job_state e WHERE e.job_id=jobs.id AND e.child_job_id IS NOT NULL) THEN clock_timestamp() ELSE finished_at END WHERE id=$1::uuid RETURNING `+jobColumns, id))
	if err != nil {
		return j, err
	}
	var child *string
	if err = tx.QueryRow(ctx, `SELECT (SELECT child_job_id::text FROM nfo_export_job_state WHERE job_id=$1::uuid)`, id).Scan(&child); err != nil {
		return j, storageError(err)
	}
	if child != nil {
		err = requestNFOExportChildCancel(ctx, tx, *child)
	}
	return j, err
}
