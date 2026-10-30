package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
	"github.com/jackc/pgx/v5"
)

var (
	_ app.MetadataRefreshJobRepository       = (*Store)(nil)
	_ app.MetadataRefreshExecutionRepository = (*Store)(nil)
)

// insertMetadataRefreshJob creates the job and its progress row together.
func insertMetadataRefreshJob(ctx context.Context, tx pgx.Tx, library string, actor *string, key, priority, trigger string, policy domain.JobPolicy) (domain.Job, error) {
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,$2::uuid,$3,'metadata_refresh',$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+jobColumns,
		library, actor, key, priority, policy.QueueLimit, policy.HistoryLimit, policy.MaxEntries, policy.MaxDirectories, policy.MaxAttempts, policy.MissingCountLimit, policy.MissingPercentLimit))
	if err != nil {
		return domain.Job{}, err
	}
	tracing.Remember(ctx, tracing.LinkJob, job.ID)
	if _, err = tx.Exec(ctx, `INSERT INTO metadata_refresh_job_state(job_id,library_id,trigger,updated_at) VALUES($1::uuid,$2::uuid,$3,clock_timestamp())`, job.ID, library, trigger); err != nil {
		return domain.Job{}, storageError(err)
	}
	return job, nil
}

// SubmitMetadataRefreshJob admits a metadata refresh of one library, which
// must have no active job. Replay of the same key, library and priority
// returns the first job.
func (s *Store) SubmitMetadataRefreshJob(ctx context.Context, actor domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error) {
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
		if old.Kind != domain.JobMetadataRefresh || old.LibraryID != library || old.Priority != priority {
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
	job, err := insertMetadataRefreshJob(ctx, tx, library, &actor.UserID, key, priority, domain.MetadataRefreshTriggerManual, policy)
	if err != nil {
		return domain.Job{}, false, err
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "metadata_refresh.submitted", job.ID, nil, map[string]any{"libraryId": library, "priority": priority, "trigger": domain.MetadataRefreshTriggerManual}); err != nil {
		return domain.Job{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, storageError(tx.Commit(ctx))
}

// enqueueAutoMetadataRefresh runs inside the FinishCatalogSync transaction
// after a successful synchronisation became terminal. Libraries opt in; a
// busy library or a full queue defers the refresh with an audit entry. The
// refresh runs as the system actor of the synchronisation, so it neither
// depends on who started the synchronisation nor on a live session. It works
// under a savepoint: any failure is rolled back to it and never fails the
// synchronisation that triggered it.
func enqueueAutoMetadataRefresh(ctx context.Context, tx pgx.Tx, sync domain.JobLease) error {
	var present bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('metadata_refresh_settings') IS NOT NULL`).Scan(&present); err != nil || !present {
		return storageError(err)
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	deferred := func(reason string) error {
		return auditAccount(ctx, savepoint, domain.Actor{}, "metadata_refresh.deferred", sync.Job.ID, nil, map[string]any{"libraryId": sync.Job.LibraryID, "reason": reason})
	}
	err = func() error {
		var auto bool
		err := savepoint.QueryRow(ctx, `SELECT auto FROM metadata_refresh_settings WHERE library_id=$1::uuid`, sync.Job.LibraryID).Scan(&auto)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !auto {
			return nil
		}
		if err != nil {
			return storageError(err)
		}
		switch err = libraryAdmission(ctx, savepoint, sync.Job.LibraryID, sync.Policy); {
		case errors.Is(err, domain.ErrJobBusy):
			return deferred("job_busy")
		case errors.Is(err, domain.ErrJobQueueFull):
			return deferred("job_queue_full")
		case err != nil:
			return err
		}
		system := domain.SystemActor(domain.SystemTriggerCatalogSync, sync.Job.ID)
		job, err := insertMetadataRefreshJob(ctx, savepoint, sync.Job.LibraryID, nil, "metadata-refresh:"+sync.Job.ID, domain.JobPriorityBackground, domain.MetadataRefreshTriggerCatalogSync, sync.Policy)
		if err != nil {
			return err
		}
		if err = markSystemJob(ctx, savepoint, job.ID, system); err != nil {
			return err
		}
		return auditAccount(ctx, savepoint, system, "metadata_refresh.submitted", job.ID, nil, map[string]any{"libraryId": sync.Job.LibraryID, "priority": domain.JobPriorityBackground, "trigger": domain.MetadataRefreshTriggerCatalogSync, "sourceJobId": sync.Job.ID})
	}()
	if err != nil {
		_ = savepoint.Rollback(ctx)
		return nil
	}
	return storageError(savepoint.Commit(ctx))
}

// GetMetadataRefreshState reads the progress of a metadata_refresh job. A job
// of another kind has no state row and is domain.ErrNotFound.
func (s *Store) GetMetadataRefreshState(ctx context.Context, actor domain.Actor, job string) (domain.MetadataRefreshJobState, error) {
	if ctx == nil {
		return domain.MetadataRefreshJobState{}, domain.ErrInvalid
	}
	if !domain.ValidID(job) {
		return domain.MetadataRefreshJobState{}, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.MetadataRefreshJobState{}, err
	}
	defer tx.Rollback(ctx)
	var v domain.MetadataRefreshJobState
	err = tx.QueryRow(ctx, `SELECT job_id::text,library_id::text,trigger,cursor_item_id::text,applied,unchanged,review,unmatched,skipped,failed,updated_at FROM metadata_refresh_job_state WHERE job_id=$1::uuid`, job).
		Scan(&v.JobID, &v.LibraryID, &v.Trigger, &v.CursorItemID, &v.Applied, &v.Unchanged, &v.Review, &v.Unmatched, &v.Skipped, &v.Failed, &v.UpdatedAt)
	if err != nil {
		return domain.MetadataRefreshJobState{}, storageError(err)
	}
	v.UpdatedAt = v.UpdatedAt.UTC()
	return v, storageError(tx.Commit(ctx))
}

// readMetadataRefreshSettings reads a library's settings, the defaults when
// it has none; an unknown library is domain.ErrNotFound.
func readMetadataRefreshSettings(ctx context.Context, tx pgx.Tx, library string, lock bool) (domain.MetadataRefreshSettings, error) {
	query := `SELECT s.auto,s.auto_apply,s.sources FROM libraries l LEFT JOIN metadata_refresh_settings s ON s.library_id=l.id WHERE l.id=$1::uuid`
	if lock {
		query += ` FOR UPDATE OF l`
	}
	var auto *bool
	var threshold *string
	var sources []string
	if err := tx.QueryRow(ctx, query, library).Scan(&auto, &threshold, &sources); err != nil {
		return domain.MetadataRefreshSettings{}, storageError(err)
	}
	v := domain.DefaultMetadataRefreshSettings(library)
	if auto != nil && threshold != nil {
		v.Auto, v.AutoApply, v.Sources = *auto, *threshold, sources
	}
	return v, nil
}

// GetMetadataRefreshSettings implements app.MetadataRefreshJobRepository.
func (s *Store) GetMetadataRefreshSettings(ctx context.Context, actor domain.Actor, library string) (domain.MetadataRefreshSettings, error) {
	if ctx == nil {
		return domain.MetadataRefreshSettings{}, domain.ErrInvalid
	}
	if !domain.ValidID(library) {
		return domain.MetadataRefreshSettings{}, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.MetadataRefreshSettings{}, err
	}
	defer tx.Rollback(ctx)
	v, err := readMetadataRefreshSettings(ctx, tx, library, false)
	if err != nil {
		return domain.MetadataRefreshSettings{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

// PutMetadataRefreshSettings implements app.MetadataRefreshJobRepository.
func (s *Store) PutMetadataRefreshSettings(ctx context.Context, actor domain.Actor, settings domain.MetadataRefreshSettings) (domain.MetadataRefreshSettings, error) {
	if ctx == nil || !domain.ValidMetadataRefreshSettings(settings) {
		return domain.MetadataRefreshSettings{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.MetadataRefreshSettings{}, err
	}
	defer tx.Rollback(ctx)
	before, err := readMetadataRefreshSettings(ctx, tx, settings.LibraryID, true)
	if err != nil {
		return domain.MetadataRefreshSettings{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO metadata_refresh_settings(library_id,auto,auto_apply,sources,updated_at) VALUES($1::uuid,$2,$3,$4,clock_timestamp())
 ON CONFLICT(library_id) DO UPDATE SET auto=EXCLUDED.auto,auto_apply=EXCLUDED.auto_apply,sources=EXCLUDED.sources,updated_at=EXCLUDED.updated_at`,
		settings.LibraryID, settings.Auto, settings.AutoApply, settings.Sources); err != nil {
		return domain.MetadataRefreshSettings{}, storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "library.metadata_refresh_changed", settings.LibraryID, before, settings); err != nil {
		return domain.MetadataRefreshSettings{}, err
	}
	return settings, storageError(tx.Commit(ctx))
}

const metadataRefreshReviewColumns = `r.item_id::text,r.library_id::text,i.kind,i.title,COALESCE(m.revision,1),r.state,r.resource,r.confidence,r.reason,r.query_title,r.query_year,r.candidates,r.job_id::text,r.updated_at`

const metadataRefreshReviewFrom = ` FROM metadata_refresh_reviews r JOIN items i ON i.id=r.item_id LEFT JOIN item_metadata_state m ON m.item_id=r.item_id`

func scanMetadataRefreshReview(row pgx.Row) (domain.MetadataRefreshReview, error) {
	var v domain.MetadataRefreshReview
	var candidates []byte
	if err := row.Scan(&v.ItemID, &v.LibraryID, &v.Kind, &v.Title, &v.Revision, &v.State, &v.Resource, &v.Confidence, &v.Reason, &v.QueryTitle, &v.QueryYear, &candidates, &v.JobID, &v.UpdatedAt); err != nil {
		return domain.MetadataRefreshReview{}, storageError(err)
	}
	if err := json.Unmarshal(candidates, &v.Candidates); err != nil {
		return domain.MetadataRefreshReview{}, domain.ErrDatabase
	}
	v.UpdatedAt = v.UpdatedAt.UTC()
	return v, nil
}

// ListMetadataRefreshReviews implements app.MetadataRefreshJobRepository.
func (s *Store) ListMetadataRefreshReviews(ctx context.Context, actor domain.Actor, library, state, cursor string, limit int) ([]domain.MetadataRefreshReview, error) {
	if ctx == nil || cursor != "" && !domain.ValidID(cursor) || limit < 1 || limit > 100 || state != "" && state != domain.MetadataReviewPending && state != domain.MetadataReviewRejected {
		return nil, domain.ErrInvalid
	}
	if !domain.ValidID(library) {
		return nil, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT true FROM libraries WHERE id=$1::uuid`, library).Scan(&exists); err != nil {
		return nil, storageError(err)
	}
	rows, err := tx.Query(ctx, `SELECT `+metadataRefreshReviewColumns+metadataRefreshReviewFrom+`
 WHERE r.library_id=$1::uuid AND ($2='' OR r.state=$2) AND r.item_id>COALESCE($3::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY r.item_id LIMIT $4`, library, state, nullableID(cursor), limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	result := []domain.MetadataRefreshReview{}
	for rows.Next() {
		v, err := scanMetadataRefreshReview(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	rows.Close()
	return result, storageError(tx.Commit(ctx))
}

// GetMetadataRefreshReview implements app.MetadataRefreshJobRepository.
func (s *Store) GetMetadataRefreshReview(ctx context.Context, actor domain.Actor, item string) (domain.MetadataRefreshReview, error) {
	if ctx == nil {
		return domain.MetadataRefreshReview{}, domain.ErrInvalid
	}
	if !domain.ValidID(item) {
		return domain.MetadataRefreshReview{}, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.MetadataRefreshReview{}, err
	}
	defer tx.Rollback(ctx)
	v, err := scanMetadataRefreshReview(tx.QueryRow(ctx, `SELECT `+metadataRefreshReviewColumns+metadataRefreshReviewFrom+` WHERE r.item_id=$1::uuid`, item))
	if err != nil {
		return domain.MetadataRefreshReview{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

// RejectMetadataRefreshReview implements app.MetadataRefreshJobRepository.
func (s *Store) RejectMetadataRefreshReview(ctx context.Context, actor domain.Actor, item string) (domain.MetadataRefreshReview, error) {
	if ctx == nil {
		return domain.MetadataRefreshReview{}, domain.ErrInvalid
	}
	if !domain.ValidID(item) {
		return domain.MetadataRefreshReview{}, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.MetadataRefreshReview{}, err
	}
	defer tx.Rollback(ctx)
	var state string
	if err = tx.QueryRow(ctx, `SELECT state FROM metadata_refresh_reviews WHERE item_id=$1::uuid FOR UPDATE`, item).Scan(&state); err != nil {
		return domain.MetadataRefreshReview{}, storageError(err)
	}
	if state != domain.MetadataReviewPending {
		return domain.MetadataRefreshReview{}, domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE metadata_refresh_reviews SET state='rejected',rejected_at=clock_timestamp(),rejected_by=$2::uuid,updated_at=clock_timestamp() WHERE item_id=$1::uuid`, item, actor.UserID); err != nil {
		return domain.MetadataRefreshReview{}, storageError(err)
	}
	v, err := scanMetadataRefreshReview(tx.QueryRow(ctx, `SELECT `+metadataRefreshReviewColumns+metadataRefreshReviewFrom+` WHERE r.item_id=$1::uuid`, item))
	if err != nil {
		return domain.MetadataRefreshReview{}, err
	}
	if err = auditAccount(ctx, tx, actor, "metadata_refresh.review_rejected", item, map[string]any{"state": domain.MetadataReviewPending}, map[string]any{"state": domain.MetadataReviewRejected, "reason": v.Reason, "confidence": v.Confidence}); err != nil {
		return domain.MetadataRefreshReview{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

// ResolveMetadataRefreshReview implements app.MetadataRefreshJobRepository:
// the chosen candidate was applied, so the pending review goes away.
func (s *Store) ResolveMetadataRefreshReview(ctx context.Context, actor domain.Actor, item string, provider int32, revision int64) error {
	if ctx == nil || !domain.ValidID(item) || provider <= 0 || revision < 1 {
		return domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM metadata_refresh_reviews WHERE item_id=$1::uuid AND state='pending'`, item); err != nil {
		return storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "metadata_refresh.review_accepted", item, nil, map[string]any{"providerId": provider, "revision": revision}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// fencedMetadataRefresh fences the lease and requires a metadata_refresh job.
func fencedMetadataRefresh(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, error) {
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.JobLease{}, err
	}
	if current.Job.Kind != domain.JobMetadataRefresh {
		return domain.JobLease{}, domain.ErrInvalid
	}
	return current, nil
}

// NextMetadataRefreshBatch implements app.MetadataRefreshExecutionRepository.
// A job queued by a schedule or after a synchronisation runs as its system
// actor and needs no user. A manually submitted job runs as the administrator
// behind it, bound to their longest-lived live session, so it outlives the
// session that submitted it but not the administrator's rights.
func (s *Store) NextMetadataRefreshBatch(ctx context.Context, lease domain.JobLease, limit int) (domain.MetadataRefreshBatch, error) {
	if ctx == nil || limit < 1 || limit > 1000 {
		return domain.MetadataRefreshBatch{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.MetadataRefreshBatch{}, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedMetadataRefresh(ctx, tx, lease)
	if err != nil {
		return domain.MetadataRefreshBatch{}, err
	}
	if current.Job.CancelRequested {
		return domain.MetadataRefreshBatch{}, context.Canceled
	}
	var library string
	var cursor *string
	if err = tx.QueryRow(ctx, `SELECT library_id::text,cursor_item_id::text FROM metadata_refresh_job_state WHERE job_id=$1::uuid`, lease.Job.ID).Scan(&library, &cursor); err != nil {
		return domain.MetadataRefreshBatch{}, storageError(err)
	}
	var batch domain.MetadataRefreshBatch
	system, ok, err := jobSystemActor(ctx, tx, lease.Job.ID)
	if err != nil {
		return domain.MetadataRefreshBatch{}, err
	}
	if ok {
		batch.Actor = system
	} else {
		err = tx.QueryRow(ctx, `SELECT u.id::text,s.id::text FROM jobs j JOIN users u ON u.id=j.actor_id JOIN sessions s ON s.user_id=u.id
 WHERE j.id=$1::uuid AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL AND s.revoked_at IS NULL AND s.expires_at>now()
 ORDER BY s.expires_at DESC,s.id LIMIT 1`, lease.Job.ID).Scan(&batch.Actor.UserID, &batch.Actor.SessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.MetadataRefreshBatch{}, domain.ErrForbidden
		}
		if err != nil {
			return domain.MetadataRefreshBatch{}, storageError(err)
		}
	}
	if batch.Settings, err = readMetadataRefreshSettings(ctx, tx, library, false); err != nil {
		return domain.MetadataRefreshBatch{}, err
	}
	rows, err := tx.Query(ctx, `SELECT i.id::text,i.kind,COALESCE(c.scan_title,''),COALESCE(c.year,0),COALESCE(k.kind_locked,false),COALESCE(r.state,'')
 FROM items i LEFT JOIN catalog_scan_items c ON c.item_id=i.id LEFT JOIN item_identity_corrections k ON k.item_id=i.id LEFT JOIN metadata_refresh_reviews r ON r.item_id=i.id
 WHERE i.library_id=$1::uuid AND i.kind IN ('Movie','HomeVideo','Series') AND ($2::uuid IS NULL OR i.id>$2::uuid) ORDER BY i.id LIMIT $3`, library, cursor, limit)
	if err != nil {
		return domain.MetadataRefreshBatch{}, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var t domain.MetadataRefreshTarget
		if err = rows.Scan(&t.ItemID, &t.Kind, &t.ScanTitle, &t.ScanYear, &t.KindLocked, &t.Review); err != nil {
			return domain.MetadataRefreshBatch{}, storageError(err)
		}
		batch.Items = append(batch.Items, t)
	}
	if err = rows.Err(); err != nil {
		return domain.MetadataRefreshBatch{}, storageError(err)
	}
	rows.Close()
	return batch, storageError(tx.Commit(ctx))
}

// RecordMetadataRefresh implements app.MetadataRefreshExecutionRepository.
// A review replaces a pending one but never a rejection recorded meanwhile;
// such an item, or one deleted meanwhile, counts as skipped. The cursor,
// counters and the job's progress columns change in the same fenced
// transaction.
func (s *Store) RecordMetadataRefresh(ctx context.Context, lease domain.JobLease, results []domain.MetadataRefreshResult) error {
	if ctx == nil || len(results) == 0 || len(results) > 1000 {
		return domain.ErrInvalid
	}
	previous := ""
	for _, r := range results {
		if !domain.ValidMetadataRefreshResult(r) || r.ItemID <= previous {
			return domain.ErrInvalid
		}
		previous = r.ItemID
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = fencedMetadataRefresh(ctx, tx, lease); err != nil {
		return err
	}
	var library string
	if err = tx.QueryRow(ctx, `SELECT library_id::text FROM metadata_refresh_job_state WHERE job_id=$1::uuid`, lease.Job.ID).Scan(&library); err != nil {
		return storageError(err)
	}
	var counts domain.MetadataRefreshCounts
	for _, r := range results {
		outcome := r.Outcome
		switch outcome {
		case domain.MetadataRefreshReviewed:
			stored, err := recordMetadataRefreshReview(ctx, tx, lease.Job.ID, library, r)
			if err != nil {
				return err
			}
			if !stored {
				outcome = domain.MetadataRefreshSkipped
			}
		case domain.MetadataRefreshApplied, domain.MetadataRefreshUnchanged:
			if _, err = tx.Exec(ctx, `DELETE FROM metadata_refresh_reviews WHERE item_id=$1::uuid AND library_id=$2::uuid AND state='pending'`, r.ItemID, library); err != nil {
				return storageError(err)
			}
		}
		switch outcome {
		case domain.MetadataRefreshApplied:
			counts.Applied++
		case domain.MetadataRefreshUnchanged:
			counts.Unchanged++
		case domain.MetadataRefreshReviewed:
			counts.Review++
		case domain.MetadataRefreshUnmatched:
			counts.Unmatched++
		case domain.MetadataRefreshSkipped:
			counts.Skipped++
		default:
			counts.Failed++
		}
	}
	var applied, unchanged, review, unmatched, skipped, failed int64
	err = tx.QueryRow(ctx, `UPDATE metadata_refresh_job_state SET cursor_item_id=$2::uuid,applied=applied+$3,unchanged=unchanged+$4,review=review+$5,unmatched=unmatched+$6,skipped=skipped+$7,failed=failed+$8,updated_at=clock_timestamp()
 WHERE job_id=$1::uuid AND (cursor_item_id IS NULL OR cursor_item_id<$9::uuid) RETURNING applied,unchanged,review,unmatched,skipped,failed`,
		lease.Job.ID, results[len(results)-1].ItemID, counts.Applied, counts.Unchanged, counts.Review, counts.Unmatched, counts.Skipped, counts.Failed, results[0].ItemID).
		Scan(&applied, &unchanged, &review, &unmatched, &skipped, &failed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrConflict
		}
		return storageError(err)
	}
	if err = guardedJobUpdate(ctx, tx, lease, `UPDATE jobs SET files=$2,skipped=$3 WHERE id=$1::uuid`, lease.Job.ID, applied+unchanged+review+unmatched, skipped+failed); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// recordMetadataRefreshReview stores one review. It reports false when the
// item is gone or its review was rejected.
func recordMetadataRefreshReview(ctx context.Context, tx pgx.Tx, job, library string, r domain.MetadataRefreshResult) (bool, error) {
	v := r.Review
	candidates, err := json.Marshal(v.Candidates)
	if err != nil {
		return false, domain.ErrInvalid
	}
	var year *int
	if v.QueryYear != 0 {
		year = &v.QueryYear
	}
	tag, err := tx.Exec(ctx, `INSERT INTO metadata_refresh_reviews(item_id,library_id,state,resource,confidence,reason,query_title,query_year,candidates,job_id,updated_at)
 SELECT i.id,i.library_id,'pending',$3,$4,$5,$6,$7,$8::jsonb,$9::uuid,clock_timestamp() FROM items i WHERE i.id=$1::uuid AND i.library_id=$2::uuid
 ON CONFLICT(item_id) DO UPDATE SET resource=EXCLUDED.resource,confidence=EXCLUDED.confidence,reason=EXCLUDED.reason,query_title=EXCLUDED.query_title,query_year=EXCLUDED.query_year,
 candidates=EXCLUDED.candidates,job_id=EXCLUDED.job_id,updated_at=EXCLUDED.updated_at WHERE metadata_refresh_reviews.state='pending'`,
		r.ItemID, library, v.Resource, v.Confidence, v.Reason, v.QueryTitle, year, string(candidates), job)
	if err != nil {
		return false, storageError(err)
	}
	return tag.RowsAffected() == 1, nil
}

// FinishMetadataRefresh makes a metadata_refresh job terminal under its lease
// fence.
func (s *Store) FinishMetadataRefresh(ctx context.Context, l domain.JobLease, state, code string) error {
	if !validJobError(state, code) && !(state == domain.JobFailed && code == "metadata_refresh_failed") {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedMetadataRefresh(ctx, tx, l)
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
