package postgres

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

const jobColumns = `id::text,library_id::text,kind,state,priority,attempts,cancel_requested,files,directories,skipped,bytes,missing,review_required,error_code,created_at,started_at,finished_at`
const listInventorySQL = `SELECT id::text,root_id::text,path,kind,size,modified_unix_nano FROM job_inventory WHERE job_id=$1::uuid AND id>COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY job_inventory.id LIMIT $3`

func jobScanTargets(j *domain.Job) []any {
	return []any{&j.ID, &j.LibraryID, &j.Kind, &j.State, &j.Priority, &j.Attempts, &j.CancelRequested, &j.Files, &j.Directories, &j.Skipped, &j.Bytes, &j.Missing, &j.ReviewRequired, &j.ErrorCode, &j.CreatedAt, &j.StartedAt, &j.FinishedAt}
}
func scanJob(row pgx.Row) (domain.Job, error) {
	var j domain.Job
	err := row.Scan(jobScanTargets(&j)...)
	return j, storageError(err)
}
func validJobPolicy(p domain.JobPolicy) bool {
	return p.QueueLimit >= 1 && p.QueueLimit <= 1000 && p.HistoryLimit >= 1 && p.HistoryLimit <= 100 && p.MaxEntries >= 100 && p.MaxEntries <= 500000 && p.MaxDirectories >= 1 && p.MaxDirectories <= 100000 && p.MaxAttempts >= 1 && p.MaxAttempts <= 10 && p.MissingCountLimit >= 1 && p.MissingCountLimit <= 500000 && p.MissingPercentLimit >= 1 && p.MissingPercentLimit <= 100
}
func validJobKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, r := range key {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
func validJobState(state string) bool {
	return state == domain.JobQueued || state == domain.JobRunning || state == domain.JobSucceeded || state == domain.JobFailed || state == domain.JobCancelled
}
func lockJobs(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()),17481204)`)
	return storageError(err)
}
func (s *Store) jobTransaction(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='1500ms'; SET LOCAL statement_timeout='2000ms'`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, storageError(err)
	}
	if err = lockJobs(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func (s *Store) authorizedJobs(ctx context.Context, a domain.Actor) (pgx.Tx, error) {
	tx, _, err := s.authorizedTransaction(ctx, a, true)
	if err != nil {
		return nil, err
	}
	if err = lockJobs(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func trimJobs(ctx context.Context, tx pgx.Tx, limit int) error {
	if _, err := releaseExpiredProbeLeases(ctx, tx, domain.ProbeSweepMax); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM jobs WHERE id IN (SELECT id FROM jobs WHERE state IN ('succeeded','failed','cancelled') ORDER BY finished_at DESC,id DESC OFFSET $1)`, limit)
	return storageError(err)
}

func (s *Store) SubmitJob(ctx context.Context, a domain.Actor, libraryID, key, priority string, p domain.JobPolicy) (domain.Job, bool, error) {
	return s.submitJob(ctx, a, libraryID, "", key, priority, p)
}
func (s *Store) RetryJob(ctx context.Context, a domain.Actor, id, key string, p domain.JobPolicy) (domain.Job, bool, error) {
	if !domain.ValidID(id) {
		return domain.Job{}, false, domain.ErrNotFound
	}
	return s.submitJob(ctx, a, "", id, key, "", p)
}
func (s *Store) submitJob(ctx context.Context, a domain.Actor, libraryID, parent, key, priority string, p domain.JobPolicy) (domain.Job, bool, error) {
	if !validJobPolicy(p) || !validJobKey(key) || (parent == "" && (!domain.ValidID(libraryID) || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground))) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	if err = trimJobs(ctx, tx, p.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	old, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE actor_id=$1::uuid AND idempotency_key=$2`, a.UserID, key))
	if err == nil {
		var originalParent string
		if err = tx.QueryRow(ctx, `SELECT COALESCE(parent_id::text,'') FROM jobs WHERE id=$1::uuid`, old.ID).Scan(&originalParent); err != nil {
			return domain.Job{}, false, storageError(err)
		}
		if originalParent != parent || parent == "" && (old.LibraryID != libraryID || old.Priority != priority) {
			return domain.Job{}, false, domain.ErrConflict
		}
		return old, true, storageError(tx.Commit(ctx))
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Job{}, false, err
	}
	if parent != "" {
		old, err = scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=$1::uuid`, parent))
		if err != nil {
			return domain.Job{}, false, err
		}
		if old.State != domain.JobFailed && old.State != domain.JobCancelled {
			return domain.Job{}, false, domain.ErrConflict
		}
		libraryID, priority = old.LibraryID, old.Priority
	}
	var exists, busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM libraries WHERE id=$1::uuid),EXISTS(SELECT 1 FROM jobs WHERE library_id=$1::uuid AND state IN ('queued','running'))`, libraryID).Scan(&exists, &busy); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if !exists {
		return domain.Job{}, false, domain.ErrNotFound
	}
	if busy {
		return domain.Job{}, false, domain.ErrJobBusy
	}
	var active, roots int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE state IN ('queued','running')`).Scan(&active); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if active >= p.QueueLimit {
		return domain.Job{}, false, domain.ErrJobQueueFull
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM library_roots WHERE library_id=$1::uuid`, libraryID).Scan(&roots); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if roots == 0 {
		return domain.Job{}, false, domain.ErrScanUnavailable
	}
	if roots > p.MaxDirectories {
		return domain.Job{}, false, domain.ErrScanLimit
	}
	j, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,parent_id,priority,directory_total,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,$2::uuid,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING `+jobColumns, libraryID, a.UserID, key, parent, priority, roots, p.QueueLimit, p.HistoryLimit, p.MaxEntries, p.MaxDirectories, p.MaxAttempts, p.MissingCountLimit, p.MissingPercentLimit))
	if err != nil {
		return domain.Job{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_directories(job_id,root_id,path) SELECT $1::uuid,id,'.' FROM library_roots WHERE library_id=$2::uuid`, j.ID, libraryID); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if err = auditAccount(ctx, tx, a, "job.submitted", j.ID, nil, j); err != nil {
		return domain.Job{}, false, err
	}
	return j, false, storageError(tx.Commit(ctx))
}
func (s *Store) GetJob(ctx context.Context, a domain.Actor, id string) (domain.Job, error) {
	if !domain.ValidID(id) {
		return domain.Job{}, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.Job{}, err
	}
	defer tx.Rollback(ctx)
	j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=$1::uuid`, id))
	if err != nil {
		return j, err
	}
	return j, storageError(tx.Commit(ctx))
}
func (s *Store) CancelJob(ctx context.Context, a domain.Actor, id string) (domain.Job, error) {
	if !domain.ValidID(id) {
		return domain.Job{}, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.Job{}, err
	}
	defer tx.Rollback(ctx)
	before, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return before, err
	}
	if before.State != domain.JobQueued && before.State != domain.JobRunning {
		return before, storageError(tx.Commit(ctx))
	}
	j, err := scanJob(tx.QueryRow(ctx, `UPDATE jobs SET cancel_requested=true,state=CASE WHEN state='queued' THEN 'cancelled' ELSE state END,finished_at=CASE WHEN state='queued' THEN clock_timestamp() ELSE finished_at END WHERE id=$1::uuid RETURNING `+jobColumns, id))
	if err != nil {
		return j, err
	}
	if err = auditAccount(ctx, tx, a, "job.cancel_requested", id, before, j); err != nil {
		return j, err
	}
	var history int
	if err = tx.QueryRow(ctx, `SELECT history_limit FROM jobs WHERE id=$1::uuid`, id).Scan(&history); err != nil {
		return j, storageError(err)
	}
	if err = trimJobs(ctx, tx, history); err != nil {
		return j, err
	}
	return j, storageError(tx.Commit(ctx))
}
func validJobPage(cursor string, limit int) bool {
	return (cursor == "" || domain.ValidID(cursor)) && limit >= 1 && limit <= 100
}
func (s *Store) ListJobs(ctx context.Context, a domain.Actor, cursor string, limit int, state string) ([]domain.Job, error) {
	if !validJobPage(cursor, limit) || (state != "" && !validJobState(state)) {
		return nil, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id>COALESCE(NULLIF($1,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND ($3='' OR state=$3) ORDER BY jobs.id LIMIT $2`, cursor, limit, state)
	if err != nil {
		return nil, storageError(err)
	}
	result := make([]domain.Job, 0)
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		result = append(result, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	return result, storageError(tx.Commit(ctx))
}
func (s *Store) ListInventory(ctx context.Context, a domain.Actor, id, cursor string, limit int) ([]domain.InventoryEntry, error) {
	if !domain.ValidID(id) || !validJobPage(cursor, limit) {
		return nil, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=$1::uuid`, id)); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, listInventorySQL, id, cursor, limit)
	if err != nil {
		return nil, storageError(err)
	}
	result := make([]domain.InventoryEntry, 0)
	for rows.Next() {
		var e domain.InventoryEntry
		if err = rows.Scan(&e.ID, &e.RootID, &e.Path, &e.Kind, &e.Size, &e.ModifiedUnixNano); err != nil {
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
func (s *Store) ListLibraries(ctx context.Context, a domain.Actor, cursor string, limit int) ([]domain.LibrarySummary, error) {
	if !validJobPage(cursor, limit) {
		return nil, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT l.id::text,l.name,(SELECT count(*) FROM library_roots r WHERE r.library_id=l.id) FROM libraries l WHERE l.id>COALESCE(NULLIF($1,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY l.id LIMIT $2`, cursor, limit)
	if err != nil {
		return nil, storageError(err)
	}
	result := make([]domain.LibrarySummary, 0)
	for rows.Next() {
		var l domain.LibrarySummary
		if err = rows.Scan(&l.ID, &l.Name, &l.Roots); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		result = append(result, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	return result, storageError(tx.Commit(ctx))
}

// RegisterLibrary is for a trusted local database operator. Filesystem root
// validation belongs to the caller; no HTTP endpoint accepts this path.
func (s *Store) RegisterLibrary(ctx context.Context, name, rootPath string) (domain.LibraryRegistration, error) {
	if !validText(name, 128, false) || strings.TrimSpace(name) != name || !filepath.IsAbs(rootPath) || !validText(rootPath, 4096, false) {
		return domain.LibraryRegistration{}, domain.ErrInvalid
	}
	tx, err := s.accountTransaction(ctx)
	if err != nil {
		return domain.LibraryRegistration{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJobs(ctx, tx); err != nil {
		return domain.LibraryRegistration{}, err
	}
	var r domain.LibraryRegistration
	err = tx.QueryRow(ctx, `INSERT INTO libraries(name) VALUES($1) ON CONFLICT(name) DO UPDATE SET name=EXCLUDED.name RETURNING id::text,name`, name).Scan(&r.Library.ID, &r.Library.Name)
	if err != nil {
		return r, storageError(err)
	}
	err = tx.QueryRow(ctx, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, r.Library.ID, rootPath).Scan(&r.RootID)
	if err != nil {
		return r, storageError(err)
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM library_roots WHERE library_id=$1::uuid`, r.Library.ID).Scan(&r.Library.Roots); err != nil {
		return r, storageError(err)
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "library.registered", r.Library.ID, nil, r); err != nil {
		return r, err
	}
	return r, storageError(tx.Commit(ctx))
}
