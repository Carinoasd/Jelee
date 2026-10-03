package postgres

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func catalogImportLease(ctx context.Context, tx pgx.Tx, lease domain.JobLease) (domain.JobLease, domain.Actor, error) {
	current, err := fencedJob(ctx, tx, lease)
	if err != nil {
		return current, domain.Actor{}, err
	}
	if current.Job.Kind != domain.JobCatalogImport {
		return current, domain.Actor{}, domain.ErrInvalid
	}
	if current.Job.CancelRequested {
		return current, domain.Actor{}, context.Canceled
	}
	var actor domain.Actor
	err = tx.QueryRow(ctx, `SELECT u.id::text FROM jobs j JOIN users u ON u.id=j.actor_id WHERE j.id=$1 AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL`, lease.Job.ID).Scan(&actor.UserID)
	if err == pgx.ErrNoRows {
		return current, actor, domain.ErrForbidden
	}
	return current, actor, storageError(err)
}

func readCatalogImportTask(ctx context.Context, tx pgx.Tx, job string, sequence int) (domain.CatalogImportTask, error) {
	var task domain.CatalogImportTask
	var captured domain.InventoryImportSource
	err := tx.QueryRow(ctx, `SELECT e.sequence,COALESCE(r.source_job_id::text,''),e.entry_id::text,e.root_id::text,e.relative_path,e.size,e.modified_unix_nano,r.inventory_generation,r.baseline_revision,e.title,e.kind,COALESCE(e.parent_id::text,'') FROM catalog_import_requests r JOIN catalog_import_entries e ON e.job_id=r.job_id WHERE r.job_id=$1 AND NOT e.completed AND ($2=0 OR e.sequence=$2) ORDER BY e.sequence LIMIT 1 FOR UPDATE OF r,e`, job, sequence).Scan(&task.Sequence, &captured.JobID, &captured.EntryID, &captured.RootID, &captured.Path, &captured.Size, &captured.ModifiedUnixNano, &captured.Generation, &captured.BaselineRevision, &task.Input.Title, &task.Input.Kind, &task.Input.ParentID)
	if err != nil {
		return task, storageError(err)
	}
	if !domain.ValidID(captured.JobID) {
		return task, domain.ErrConflict
	}
	task.Source, err = readInventoryImport(ctx, tx, captured.JobID, captured.EntryID)
	if err == domain.ErrNotFound {
		return task, domain.ErrConflict
	}
	if err != nil {
		return task, err
	}
	captured.LibraryID, captured.RootPath = task.Source.LibraryID, task.Source.RootPath
	if captured != task.Source {
		return domain.CatalogImportTask{}, domain.ErrConflict
	}
	return task, nil
}

func (s *Store) NextCatalogImport(ctx context.Context, lease domain.JobLease) (domain.CatalogImportTask, error) {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.CatalogImportTask{}, err
	}
	defer tx.Rollback(ctx)
	if _, _, err = catalogImportLease(ctx, tx, lease); err != nil {
		return domain.CatalogImportTask{}, err
	}
	task, err := readCatalogImportTask(ctx, tx, lease.Job.ID, 0)
	if err == domain.ErrNotFound {
		var complete bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_import_requests r WHERE r.job_id=$1 AND r.total=(SELECT count(*) FROM catalog_import_entries e WHERE e.job_id=r.job_id AND e.completed))`, lease.Job.ID).Scan(&complete); checkErr != nil {
			return domain.CatalogImportTask{}, storageError(checkErr)
		}
		if !complete {
			return domain.CatalogImportTask{}, domain.ErrConflict
		}
	}
	if err != nil {
		return domain.CatalogImportTask{}, err
	}
	return task, storageError(tx.Commit(ctx))
}

func (s *Store) CommitCatalogImport(ctx context.Context, lease domain.JobLease, expected domain.CatalogImportTask) error {
	if expected.Sequence < 1 || expected.Sequence > domain.CatalogImportBatchLimit {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, actor, err := catalogImportLease(ctx, tx, lease)
	if err != nil {
		return err
	}
	task, err := readCatalogImportTask(ctx, tx, lease.Job.ID, expected.Sequence)
	if err != nil {
		return err
	}
	if task != expected || task.Source.LibraryID != current.Job.LibraryID {
		return domain.ErrConflict
	}
	result, err := putInventoryImport(ctx, tx, task.Source, task.Input, actor)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE catalog_import_entries SET completed=true,item_id=$3,source_id=$4 WHERE job_id=$1 AND sequence=$2 AND NOT completed`, lease.Job.ID, task.Sequence, result.ItemID, result.SourceID); err != nil {
		return storageError(err)
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET files=files+1 WHERE id=$1`, lease.Job.ID); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

func (s *Store) FinishCatalogImport(ctx context.Context, lease domain.JobLease, state, code string) error {
	if !validJobError(state, code) && !(state == domain.JobFailed && code == "catalog_import_failed") {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, lease)
	if err != nil {
		return err
	}
	if current.Job.Kind != domain.JobCatalogImport {
		return domain.ErrInvalid
	}
	if state == domain.JobSucceeded {
		if current.Job.CancelRequested {
			return domain.ErrConflict
		}
		var complete bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_import_requests r WHERE r.job_id=$1 AND r.total=(SELECT count(*) FROM catalog_import_entries e WHERE e.job_id=r.job_id AND e.completed))`, lease.Job.ID).Scan(&complete); err != nil {
			return storageError(err)
		}
		if !complete {
			return domain.ErrConflict
		}
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET state=$2,error_code=$3,finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE id=$1`, lease.Job.ID, state, code); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "catalog_import.finished", lease.Job.ID, nil, map[string]any{"state": state, "completed": current.Job.Files}); err != nil {
		return err
	}
	if err = trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
