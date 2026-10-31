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
	_ app.ImageRefreshJobRepository       = (*Store)(nil)
	_ app.ImageRefreshExecutionRepository = (*Store)(nil)
)

// insertImageRefreshJob creates the job, with the library's candidate count
// as its progress total, and its progress row together.
func insertImageRefreshJob(ctx context.Context, tx pgx.Tx, library string, actor *string, key, priority string, policy domain.JobPolicy) (domain.Job, error) {
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,progress_total,progress_total_at) VALUES($1::uuid,$2::uuid,$3,'image_refresh',$4,$5,$6,$7,$8,$9,$10,$11,
 (SELECT count(*) FROM item_images g WHERE g.library_id=$1::uuid AND `+imageRefreshCandidate+`),clock_timestamp()) RETURNING `+jobColumns,
		library, actor, key, priority, policy.QueueLimit, policy.HistoryLimit, policy.MaxEntries, policy.MaxDirectories, policy.MaxAttempts, policy.MissingCountLimit, policy.MissingPercentLimit))
	if err != nil {
		return domain.Job{}, err
	}
	tracing.Remember(ctx, tracing.LinkJob, job.ID)
	if _, err = tx.Exec(ctx, `INSERT INTO image_refresh_job_state(job_id,library_id,updated_at) VALUES($1::uuid,$2::uuid,clock_timestamp())`, job.ID, library); err != nil {
		return domain.Job{}, storageError(err)
	}
	return job, nil
}

// SubmitImageRefreshJob admits an image refresh of one library, which must
// have no active job. Replay of the same key, library and priority returns
// the first job. Its progress total is the candidate count at admission.
func (s *Store) SubmitImageRefreshJob(ctx context.Context, actor domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error) {
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
		if old.Kind != domain.JobImageRefresh || old.LibraryID != library || old.Priority != priority {
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
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT true FROM libraries WHERE id=$1::uuid FOR SHARE`, library).Scan(&exists); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if err = libraryAdmission(ctx, tx, library, policy); err != nil {
		return domain.Job{}, false, err
	}
	job, err := insertImageRefreshJob(ctx, tx, library, &actor.UserID, key, priority, policy)
	if err != nil {
		return domain.Job{}, false, err
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "image.refresh_submitted", job.ID, nil, map[string]any{"libraryId": library, "priority": priority}); err != nil {
		return domain.Job{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, storageError(tx.Commit(ctx))
}

// GetImageRefreshState reads the progress of an image_refresh job. A job of
// another kind has no state row and is domain.ErrNotFound.
func (s *Store) GetImageRefreshState(ctx context.Context, actor domain.Actor, job string) (domain.ImageRefreshJobState, error) {
	if ctx == nil {
		return domain.ImageRefreshJobState{}, domain.ErrInvalid
	}
	if !domain.ValidID(job) {
		return domain.ImageRefreshJobState{}, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.ImageRefreshJobState{}, err
	}
	defer tx.Rollback(ctx)
	var v domain.ImageRefreshJobState
	err = tx.QueryRow(ctx, `SELECT job_id::text,library_id::text,cursor_image_id::text,fetched,failed,deferred,skipped,updated_at FROM image_refresh_job_state WHERE job_id=$1::uuid`, job).
		Scan(&v.JobID, &v.LibraryID, &v.CursorImageID, &v.Fetched, &v.Failed, &v.Deferred, &v.Skipped, &v.UpdatedAt)
	if err != nil {
		return domain.ImageRefreshJobState{}, storageError(err)
	}
	v.UpdatedAt = v.UpdatedAt.UTC()
	return v, storageError(tx.Commit(ctx))
}

// fencedImageRefresh fences the lease and requires an image_refresh job.
func fencedImageRefresh(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, error) {
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.JobLease{}, err
	}
	if current.Job.Kind != domain.JobImageRefresh {
		return domain.JobLease{}, domain.ErrInvalid
	}
	return current, nil
}

// imageRefreshCandidate is the row predicate a refresh may read and write:
// a remote row without content that no person wrote or locked.
const imageRefreshCandidate = `g.source_kind='remote' AND g.content_sha256 IS NULL AND NOT g.locked AND NOT g.manual`

// NextImageRefreshBatch implements app.ImageRefreshExecutionRepository.
// Failures count only for the URL that failed; a row whose URL changed
// since is a fresh candidate.
func (s *Store) NextImageRefreshBatch(ctx context.Context, lease domain.JobLease, limit int) ([]domain.ImageRefreshCandidate, error) {
	if ctx == nil || limit < 1 || limit > 1000 {
		return nil, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedImageRefresh(ctx, tx, lease)
	if err != nil {
		return nil, err
	}
	if current.Job.CancelRequested {
		return nil, context.Canceled
	}
	var library string
	var cursor *string
	if err = tx.QueryRow(ctx, `SELECT library_id::text,cursor_image_id::text FROM image_refresh_job_state WHERE job_id=$1::uuid`, lease.Job.ID).Scan(&library, &cursor); err != nil {
		return nil, storageError(err)
	}
	rows, err := tx.Query(ctx, `SELECT g.id::text,g.item_id::text,g.remote_url,
 CASE WHEN f.remote_url=g.remote_url THEN f.failures ELSE 0 END,
 COALESCE(f.remote_url=g.remote_url AND f.retry_at>clock_timestamp(),false)
 FROM item_images g LEFT JOIN item_image_fetch_failures f ON f.image_id=g.id
 WHERE g.library_id=$1::uuid AND `+imageRefreshCandidate+` AND ($2::uuid IS NULL OR g.id>$2::uuid)
 ORDER BY g.id LIMIT $3`, library, cursor, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var batch []domain.ImageRefreshCandidate
	for rows.Next() {
		var c domain.ImageRefreshCandidate
		if err = rows.Scan(&c.ImageID, &c.ItemID, &c.URL, &c.Failures, &c.Deferred); err != nil {
			return nil, storageError(err)
		}
		batch = append(batch, c)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	rows.Close()
	return batch, storageError(tx.Commit(ctx))
}

// RecordImageRefresh implements app.ImageRefreshExecutionRepository. Results
// must be in ascending image order after the cursor. Content and failures
// are written only while the row still is a candidate with the fetched URL,
// so a concurrent lock, manual write, URL change or deletion wins and the
// result counts as skipped. The cursor, counters and the job's progress
// columns change in the same fenced transaction.
func (s *Store) RecordImageRefresh(ctx context.Context, lease domain.JobLease, results []domain.ImageRefreshResult) error {
	if ctx == nil || len(results) == 0 || len(results) > 1000 {
		return domain.ErrInvalid
	}
	previous := ""
	for _, r := range results {
		if !domain.ValidImageRefreshResult(r) || r.Candidate.ImageID <= previous {
			return domain.ErrInvalid
		}
		previous = r.Candidate.ImageID
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = fencedImageRefresh(ctx, tx, lease); err != nil {
		return err
	}
	var library string
	if err = tx.QueryRow(ctx, `SELECT library_id::text FROM image_refresh_job_state WHERE job_id=$1::uuid`, lease.Job.ID).Scan(&library); err != nil {
		return storageError(err)
	}
	var counts domain.ImageRefreshCounts
	// fetched counts the images each item gained, in result order, for
	// images.fetched (G12.1).
	var fetchedItems []string
	fetchedImages := map[string]int{}
	for _, r := range results {
		if r.Deferred {
			counts.Deferred++
			continue
		}
		applied, err := recordImageRefreshResult(ctx, tx, library, r)
		switch {
		case err != nil:
			return err
		case !applied:
			counts.Skipped++
		case r.Content != nil:
			counts.Fetched++
			if fetchedImages[r.Candidate.ItemID] == 0 {
				fetchedItems = append(fetchedItems, r.Candidate.ItemID)
			}
			fetchedImages[r.Candidate.ItemID]++
		default:
			counts.Failed++
		}
	}
	last := results[len(results)-1].Candidate.ImageID
	var fetched, failed, deferred, skipped int64
	err = tx.QueryRow(ctx, `UPDATE image_refresh_job_state SET cursor_image_id=$2::uuid,fetched=fetched+$3,failed=failed+$4,deferred=deferred+$5,skipped=skipped+$6,updated_at=clock_timestamp()
 WHERE job_id=$1::uuid AND (cursor_image_id IS NULL OR cursor_image_id<$7::uuid) RETURNING fetched,failed,deferred,skipped`,
		lease.Job.ID, last, counts.Fetched, counts.Failed, counts.Deferred, counts.Skipped, results[0].Candidate.ImageID).Scan(&fetched, &failed, &deferred, &skipped)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrConflict
		}
		return storageError(err)
	}
	if err = guardedJobUpdate(ctx, tx, lease, `UPDATE jobs SET files=$2,skipped=$3 WHERE id=$1::uuid`, lease.Job.ID, fetched, failed+deferred+skipped); err != nil {
		return err
	}
	// One images.fetched per item that gained content in this batch, in the
	// transaction that stores it; an item whose images span batches raises
	// one per batch.
	events, now := s.webhooksOn(), time.Now()
	for _, item := range fetchedItems {
		if err = appendWebhook(ctx, tx, events, domain.WebhookImagesFetched, now, domain.WebhookSubject{Kind: domain.WebhookSubjectItem, ID: item},
			map[string]any{"libraryId": library, "jobId": lease.Job.ID, "images": fetchedImages[item]}); err != nil {
			return err
		}
	}
	return storageError(tx.Commit(ctx))
}

// recordImageRefreshResult writes one fetched or failed result. It reports
// false when the row is no longer a candidate for that URL.
func recordImageRefreshResult(ctx context.Context, tx pgx.Tx, library string, r domain.ImageRefreshResult) (bool, error) {
	c := r.Candidate
	// Same order as every image writer: the item, then its image rows.
	if err := lockImageItem(ctx, tx, c.ItemID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if r.Content != nil {
		content := r.Content
		var width, height *int
		if content.Width != 0 {
			width, height = &content.Width, &content.Height
		}
		tag, err := tx.Exec(ctx, `UPDATE item_images g SET content_sha256=$4,width=$5,height=$6,format=$7,byte_size=$8,average_color=$9,dominant_color=$12,fetched_at=$10,updated_at=clock_timestamp()
 WHERE g.id=$1::uuid AND g.item_id=$2::uuid AND g.library_id=$11::uuid AND g.remote_url=$3 AND `+imageRefreshCandidate,
			c.ImageID, c.ItemID, c.URL, content.SHA256, width, height, content.Format, content.Bytes, content.AverageColor, content.FetchedAt, library, content.DominantColor)
		if err != nil {
			return false, storageError(err)
		}
		if tag.RowsAffected() != 1 {
			return false, nil
		}
		_, err = tx.Exec(ctx, `DELETE FROM item_image_fetch_failures WHERE image_id=$1::uuid`, c.ImageID)
		return true, storageError(err)
	}
	f := r.Failure
	var status *int
	if f.Status != 0 {
		status = &f.Status
	}
	tag, err := tx.Exec(ctx, `INSERT INTO item_image_fetch_failures(image_id,remote_url,failures,reason,http_status,attempted_at,retry_at)
 SELECT g.id,g.remote_url,$4,$5,$6,n.at,n.at+$7*interval '1 microsecond'
 FROM item_images g CROSS JOIN (SELECT clock_timestamp() AS at) n WHERE g.id=$1::uuid AND g.item_id=$2::uuid AND g.library_id=$8::uuid AND g.remote_url=$3 AND `+imageRefreshCandidate+`
 ON CONFLICT(image_id) DO UPDATE SET remote_url=EXCLUDED.remote_url,failures=EXCLUDED.failures,reason=EXCLUDED.reason,http_status=EXCLUDED.http_status,attempted_at=EXCLUDED.attempted_at,retry_at=EXCLUDED.retry_at`,
		c.ImageID, c.ItemID, c.URL, r.Failures, f.Reason, status, r.RetryDelay.Microseconds(), library)
	if err != nil {
		return false, storageError(err)
	}
	return tag.RowsAffected() == 1, nil
}

// FinishImageRefresh makes an image_refresh job terminal under its lease
// fence.
func (s *Store) FinishImageRefresh(ctx context.Context, l domain.JobLease, state, code string) error {
	if !validJobError(state, code) && !(state == domain.JobFailed && code == "image_refresh_failed") {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedImageRefresh(ctx, tx, l)
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
