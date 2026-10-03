package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.CatalogSyncRepository = (*Store)(nil)
var _ app.CatalogSyncExecutionRepository = (*Store)(nil)
var _ app.ScanDirectoryClaimer = (*Store)(nil)

type libraryBaseline struct {
	snapshot, revision, generation int64
	auto                           bool
}

// lockLibraryBaseline reads the accepted baseline identity under the library
// row lock that FinishJob and snapshot publication also take.
func lockLibraryBaseline(ctx context.Context, tx pgx.Tx, library string) (libraryBaseline, error) {
	var b libraryBaseline
	err := tx.QueryRow(ctx, `SELECT active_inventory_snapshot,inventory_baseline_revision,inventory_generation,catalog_sync_auto FROM libraries WHERE id=$1::uuid FOR UPDATE`, library).Scan(&b.snapshot, &b.revision, &b.generation, &b.auto)
	return b, storageError(err)
}

func libraryAdmission(ctx context.Context, tx pgx.Tx, library string, policy domain.JobPolicy) error {
	var busy bool
	var active int
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE library_id=$1::uuid AND state IN ('queued','running')),(SELECT count(*) FROM jobs WHERE state IN ('queued','running'))`, library).Scan(&busy, &active); err != nil {
		return storageError(err)
	}
	if busy {
		return domain.ErrJobBusy
	}
	if active >= policy.QueueLimit {
		return domain.ErrJobQueueFull
	}
	return nil
}

// insertCatalogSyncJob creates the job and its immutable intent together.
// An accept intent starts by publishing; a sync intent targets the current
// accepted baseline directly.
func insertCatalogSyncJob(ctx context.Context, tx pgx.Tx, library string, actor *string, key, priority, mode string, source *string, b libraryBaseline, total int64, policy domain.JobPolicy) (domain.Job, error) {
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,inventory_generation) VALUES($1::uuid,$2::uuid,$3,'catalog_sync',$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING `+jobColumns, library, actor, key, priority, policy.QueueLimit, policy.HistoryLimit, policy.MaxEntries, policy.MaxDirectories, policy.MaxAttempts, policy.MissingCountLimit, policy.MissingPercentLimit, b.generation))
	if err != nil {
		return domain.Job{}, err
	}
	phase := "sources"
	var target, targetRevision *int64
	if mode == domain.CatalogSyncModeAccept {
		phase = "publish"
	} else {
		target, targetRevision = &b.snapshot, &b.revision
	}
	if _, err = tx.Exec(ctx, `INSERT INTO catalog_sync_requests(job_id,library_id,mode,source_job_id,expected_snapshot,expected_revision,inventory_generation,phase,publish_total,target_snapshot,target_revision) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8,$9,$10,$11)`, job.ID, library, mode, source, b.snapshot, b.revision, b.generation, phase, total, target, targetRevision); err != nil {
		return domain.Job{}, storageError(err)
	}
	return job, nil
}

// AcceptInventoryMissing is the G13.4 deletion confirmation. It accepts only
// the exact reviewed result: a complete, unskipped scan of the current scope
// whose missing count against the unchanged baseline equals the reviewed and
// the administrator-confirmed count. Publication itself runs under a fenced
// catalog_sync lease. A pending or published acceptance cannot be repeated.
func (s *Store) AcceptInventoryMissing(ctx context.Context, actor domain.Actor, id string, expected int64, policy domain.JobPolicy) (domain.Job, error) {
	if ctx == nil || !domain.ValidID(id) || expected < 1 || expected > 500000 || !validJobPolicy(policy) {
		return domain.Job{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.Job{}, err
	}
	defer tx.Rollback(ctx)
	var job domain.Job
	var frozen *int64
	var ignored bool
	err = tx.QueryRow(ctx, `SELECT `+jobColumns+`,inventory_generation,ignore_requested OR EXISTS(SELECT 1 FROM job_ignore_requests g WHERE g.job_id=jobs.id) FROM jobs WHERE id=$1::uuid FOR UPDATE`, id).Scan(append(jobScanTargets(&job), &frozen, &ignored)...)
	if err != nil {
		return domain.Job{}, storageError(err)
	}
	if job.Kind != "inventory_scan" || job.State != domain.JobSucceeded || !job.ReviewRequired || job.CancelRequested || ignored || frozen == nil || job.Skipped != 0 || job.Missing < 1 || job.Missing != expected {
		return domain.Job{}, domain.ErrConflict
	}
	var syncState string
	var published bool
	err = tx.QueryRow(ctx, `SELECT a.published_at IS NOT NULL,COALESCE(j.state,'') FROM inventory_missing_acceptances a LEFT JOIN jobs j ON j.id=a.sync_job_id WHERE a.job_id=$1::uuid FOR UPDATE OF a`, id).Scan(&published, &syncState)
	switch {
	case err == nil:
		if published || syncState == domain.JobQueued || syncState == domain.JobRunning {
			return domain.Job{}, domain.ErrConflict
		}
		// An earlier decision ended before publication; it may be made again.
		if _, err = tx.Exec(ctx, `DELETE FROM inventory_missing_acceptances WHERE job_id=$1::uuid`, id); err != nil {
			return domain.Job{}, storageError(err)
		}
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return domain.Job{}, storageError(err)
	}
	baseline, err := lockLibraryBaseline(ctx, tx, job.LibraryID)
	if err != nil {
		return domain.Job{}, err
	}
	var newer bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs n JOIN jobs j ON j.id=$1::uuid WHERE n.library_id=j.library_id AND n.kind='inventory_scan' AND (n.created_at,n.id)>(j.created_at,j.id))`, id).Scan(&newer); err != nil {
		return domain.Job{}, storageError(err)
	}
	if newer || baseline.generation != *frozen {
		return domain.Job{}, domain.ErrConflict
	}
	if err = libraryAdmission(ctx, tx, job.LibraryID, policy); err != nil {
		return domain.Job{}, err
	}
	covered, skipped, err := inventoryCoverage(ctx, tx, domain.JobLease{Job: job})
	if err != nil {
		return domain.Job{}, err
	}
	if !covered || skipped {
		return domain.Job{}, domain.ErrConflict
	}
	var total, unknown, missing int64
	if err = tx.QueryRow(ctx, inventoryMissingCountsSQL, job.LibraryID, id, *frozen).Scan(&total, &unknown, &missing); err != nil {
		return domain.Job{}, storageError(err)
	}
	if unknown != 0 || missing != job.Missing {
		return domain.Job{}, domain.ErrConflict
	}
	if job.Files > 500000 {
		return domain.Job{}, domain.ErrScanLimit
	}
	var key string
	if err = tx.QueryRow(ctx, `SELECT 'accept-missing:'||gen_random_uuid()::text`).Scan(&key); err != nil {
		return domain.Job{}, storageError(err)
	}
	sync, err := insertCatalogSyncJob(ctx, tx, job.LibraryID, &actor.UserID, key, domain.JobPriorityManual, domain.CatalogSyncModeAccept, &id, baseline, job.Files, policy)
	if err != nil {
		return domain.Job{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO inventory_missing_acceptances(job_id,library_id,actor_id,sync_job_id,accepted_missing,baseline_snapshot,baseline_revision,inventory_generation) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8)`, id, job.LibraryID, actor.UserID, sync.ID, missing, baseline.snapshot, baseline.revision, baseline.generation); err != nil {
		return domain.Job{}, storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "job.missing_accepted", id, map[string]any{"missing": missing, "baselineEntries": total, "baselineRevision": baseline.revision}, map[string]any{"catalogSyncJobId": sync.ID}); err != nil {
		return domain.Job{}, err
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, err
	}
	return sync, storageError(tx.Commit(ctx))
}

// SubmitCatalogSync queues synchronisation of the current accepted baseline.
func (s *Store) SubmitCatalogSync(ctx context.Context, actor domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error) {
	if ctx == nil || !domain.ValidID(library) || !validJobKey(key) || !validJobPolicy(policy) || priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground {
		return domain.Job{}, false, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	old, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE actor_id=$1::uuid AND idempotency_key=$2`, actor.UserID, key))
	if err == nil {
		var mode string
		if old.Kind != domain.JobCatalogSync || old.LibraryID != library || old.Priority != priority {
			return domain.Job{}, false, domain.ErrConflict
		}
		if err = tx.QueryRow(ctx, `SELECT mode FROM catalog_sync_requests WHERE job_id=$1::uuid`, old.ID).Scan(&mode); err != nil || mode != domain.CatalogSyncModeSync {
			return domain.Job{}, false, domain.ErrConflict
		}
		return old, true, storageError(tx.Commit(ctx))
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Job{}, false, err
	}
	baseline, err := lockLibraryBaseline(ctx, tx, library)
	if err != nil {
		return domain.Job{}, false, err
	}
	if err = libraryAdmission(ctx, tx, library, policy); err != nil {
		return domain.Job{}, false, err
	}
	job, err := insertCatalogSyncJob(ctx, tx, library, &actor.UserID, key, priority, domain.CatalogSyncModeSync, nil, baseline, 0, policy)
	if err != nil {
		return domain.Job{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "catalog_sync.submitted", job.ID, nil, map[string]any{"libraryId": library, "baselineRevision": baseline.revision}); err != nil {
		return domain.Job{}, false, err
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, storageError(tx.Commit(ctx))
}

// enqueueAutoCatalogSync runs inside the publishing FinishJob transaction
// after the scan became terminal, so the new job targets exactly the baseline
// that transaction commits. A full queue defers synchronisation; it never
// fails the scan. Libraries opt in; the default leaves the catalog alone.
func enqueueAutoCatalogSync(ctx context.Context, tx pgx.Tx, scan domain.JobLease) error {
	var present bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('catalog_sync_requests') IS NOT NULL`).Scan(&present); err != nil || !present {
		return storageError(err)
	}
	baseline, err := lockLibraryBaseline(ctx, tx, scan.Job.LibraryID)
	if err != nil || !baseline.auto {
		return err
	}
	switch err = libraryAdmission(ctx, tx, scan.Job.LibraryID, scan.Policy); {
	case errors.Is(err, domain.ErrJobBusy), errors.Is(err, domain.ErrJobQueueFull):
		return auditAccount(ctx, tx, domain.Actor{}, "catalog_sync.deferred", scan.Job.ID, nil, map[string]any{"libraryId": scan.Job.LibraryID})
	case err != nil:
		return err
	}
	var actor *string
	if err = tx.QueryRow(ctx, `SELECT actor_id::text FROM jobs WHERE id=$1::uuid`, scan.Job.ID).Scan(&actor); err != nil {
		return storageError(err)
	}
	job, err := insertCatalogSyncJob(ctx, tx, scan.Job.LibraryID, actor, "catalog-sync:"+scan.Job.ID, domain.JobPriorityBackground, domain.CatalogSyncModeSync, nil, baseline, 0, scan.Policy)
	if err != nil {
		return err
	}
	return auditAccount(ctx, tx, domain.Actor{}, "catalog_sync.submitted", job.ID, nil, map[string]any{"libraryId": scan.Job.LibraryID, "sourceJobId": scan.Job.ID, "baselineRevision": baseline.revision})
}

func (s *Store) GetCatalogSyncReport(ctx context.Context, actor domain.Actor, id string) (domain.CatalogSyncReport, error) {
	if ctx == nil || !domain.ValidID(id) {
		return domain.CatalogSyncReport{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.CatalogSyncReport{}, err
	}
	defer tx.Rollback(ctx)
	r := domain.CatalogSyncReport{JobID: id}
	var source *string
	err = tx.QueryRow(ctx, `SELECT library_id::text,mode,phase,source_job_id::text,examined,created,updated,unchanged,protected,pending,marked_missing,removed FROM catalog_sync_requests WHERE job_id=$1::uuid`, id).Scan(&r.LibraryID, &r.Mode, &r.Phase, &source, &r.Examined, &r.Created, &r.Updated, &r.Unchanged, &r.Protected, &r.Pending, &r.MarkedMissing, &r.Removed)
	if err != nil {
		return domain.CatalogSyncReport{}, storageError(err)
	}
	if source != nil {
		r.SourceJobID = *source
	}
	return r, storageError(tx.Commit(ctx))
}

func (s *Store) GetCatalogSyncSettings(ctx context.Context, actor domain.Actor, library string) (domain.CatalogSyncSettings, error) {
	if ctx == nil || !domain.ValidID(library) {
		return domain.CatalogSyncSettings{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.CatalogSyncSettings{}, err
	}
	defer tx.Rollback(ctx)
	value := domain.CatalogSyncSettings{LibraryID: library}
	if err = tx.QueryRow(ctx, `SELECT catalog_sync_auto FROM libraries WHERE id=$1::uuid`, library).Scan(&value.Auto); err != nil {
		return domain.CatalogSyncSettings{}, storageError(err)
	}
	return value, storageError(tx.Commit(ctx))
}

func (s *Store) PutCatalogSyncSettings(ctx context.Context, actor domain.Actor, library string, auto bool) (domain.CatalogSyncSettings, error) {
	if ctx == nil || !domain.ValidID(library) {
		return domain.CatalogSyncSettings{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.CatalogSyncSettings{}, err
	}
	defer tx.Rollback(ctx)
	var before bool
	if err = tx.QueryRow(ctx, `SELECT catalog_sync_auto FROM libraries WHERE id=$1::uuid FOR UPDATE`, library).Scan(&before); err != nil {
		return domain.CatalogSyncSettings{}, storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE libraries SET catalog_sync_auto=$2 WHERE id=$1::uuid`, library, auto); err != nil {
		return domain.CatalogSyncSettings{}, storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "library.catalog_sync_changed", library, map[string]any{"auto": before}, map[string]any{"auto": auto}); err != nil {
		return domain.CatalogSyncSettings{}, err
	}
	return domain.CatalogSyncSettings{LibraryID: library, Auto: auto}, storageError(tx.Commit(ctx))
}

func (s *Store) ListCatalogPending(ctx context.Context, actor domain.Actor, library, cursor string, limit int) ([]domain.CatalogPendingEntry, error) {
	if ctx == nil || !domain.ValidID(library) || cursor != "" && !domain.ValidID(cursor) || limit < 1 || limit > 100 {
		return nil, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM libraries WHERE id=$1::uuid)`, library).Scan(&exists); err != nil {
		return nil, storageError(err)
	}
	if !exists {
		return nil, domain.ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT id::text,root_id::text,relative_path,reason,kind,confidence,title,COALESCE(year,0),season,episode,episode_end,COALESCE(absolute,0),special FROM catalog_scan_pending WHERE library_id=$1::uuid AND id>COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY id LIMIT $3`, library, cursor, limit)
	if err != nil {
		return nil, storageError(err)
	}
	result := make([]domain.CatalogPendingEntry, 0, limit)
	for rows.Next() {
		var e domain.CatalogPendingEntry
		if err = rows.Scan(&e.ID, &e.RootID, &e.Path, &e.Reason, &e.Kind, &e.Confidence, &e.Title, &e.Year, &e.Season, &e.Episode, &e.EpisodeEnd, &e.Absolute, &e.Special); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		result = append(result, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	return result, storageError(tx.Commit(ctx))
}
