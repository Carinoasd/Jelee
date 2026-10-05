package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
)

func (s *Store) SubmitCatalogImport(ctx context.Context, actor domain.Actor, sourceJob, key, priority string, items []domain.CatalogImportSelection, policy domain.JobPolicy) (domain.Job, bool, error) {
	if ctx == nil || !domain.ValidID(sourceJob) || !validJobKey(key) || !validJobPolicy(policy) || !domain.ValidCatalogImportSelections(items) || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	payload, _ := json.Marshal(struct {
		Source, Priority string
		Items            []domain.CatalogImportSelection
	}{sourceJob, priority, items})
	digest := sha256.Sum256(payload)
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	old, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE actor_id=$1 AND idempotency_key=$2`, actor.UserID, key))
	if err == nil {
		if old.Kind != domain.JobCatalogImport {
			return domain.Job{}, false, domain.ErrConflict
		}
		var retained []byte
		if err = tx.QueryRow(ctx, `SELECT intent_digest FROM catalog_import_requests WHERE job_id=$1`, old.ID).Scan(&retained); err != nil {
			return domain.Job{}, false, storageError(err)
		}
		if !bytes.Equal(retained, digest[:]) {
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
	sources := make([]domain.InventoryImportSource, len(items))
	for i, item := range items {
		sources[i], err = readInventoryImport(ctx, tx, sourceJob, item.EntryID)
		if err != nil {
			return domain.Job{}, false, err
		}
	}
	first := sources[0]
	var busy bool
	var active int
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE library_id=$1 AND state IN ('queued','running')),(SELECT count(*) FROM jobs WHERE state IN ('queued','running'))`, first.LibraryID).Scan(&busy, &active); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if busy {
		return domain.Job{}, false, domain.ErrJobBusy
	}
	if active >= policy.QueueLimit {
		return domain.Job{}, false, domain.ErrJobQueueFull
	}
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,inventory_generation) VALUES($1,$2,$3,'catalog_import',$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING `+jobColumns, first.LibraryID, actor.UserID, key, priority, policy.QueueLimit, policy.HistoryLimit, policy.MaxEntries, policy.MaxDirectories, policy.MaxAttempts, policy.MissingCountLimit, policy.MissingPercentLimit, first.Generation))
	if err != nil {
		return domain.Job{}, false, err
	}
	tracing.Remember(ctx, tracing.LinkJob, job.ID)
	if _, err = tx.Exec(ctx, `INSERT INTO catalog_import_requests(job_id,source_job_id,intent_digest,inventory_generation,baseline_revision,total) VALUES($1,$2,$3,$4,$5,$6)`, job.ID, sourceJob, digest[:], first.Generation, first.BaselineRevision, len(items)); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	for i, item := range items {
		source := sources[i]
		if source.LibraryID != first.LibraryID || source.Generation != first.Generation || source.BaselineRevision != first.BaselineRevision {
			return domain.Job{}, false, domain.ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO catalog_import_entries(job_id,sequence,entry_id,root_id,relative_path,size,modified_unix_nano,title,kind,parent_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid)`, job.ID, i+1, item.EntryID, source.RootID, source.Path, source.Size, source.ModifiedUnixNano, item.Title, item.Kind, item.ParentID); err != nil {
			return domain.Job{}, false, storageError(err)
		}
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "catalog_import.submitted", job.ID, nil, map[string]any{"sourceJobId": sourceJob, "total": len(items)}); err != nil {
		return domain.Job{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, storageError(tx.Commit(ctx))
}

func (s *Store) GetCatalogImportReport(ctx context.Context, actor domain.Actor, id string) (domain.CatalogImportReport, error) {
	if ctx == nil || !domain.ValidID(id) {
		return domain.CatalogImportReport{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.CatalogImportReport{}, err
	}
	defer tx.Rollback(ctx)
	value := domain.CatalogImportReport{JobID: id, Entries: []domain.CatalogImportEntryResult{}}
	if err = tx.QueryRow(ctx, `SELECT total FROM catalog_import_requests WHERE job_id=$1`, id).Scan(&value.Total); err != nil {
		return domain.CatalogImportReport{}, storageError(err)
	}
	rows, err := tx.Query(ctx, `SELECT entry_id::text,completed,COALESCE(item_id::text,''),COALESCE(source_id::text,'') FROM catalog_import_entries WHERE job_id=$1 ORDER BY sequence LIMIT 100`, id)
	if err != nil {
		return domain.CatalogImportReport{}, storageError(err)
	}
	for rows.Next() {
		var item domain.CatalogImportEntryResult
		if err = rows.Scan(&item.EntryID, &item.Completed, &item.ItemID, &item.SourceID); err != nil {
			rows.Close()
			return domain.CatalogImportReport{}, storageError(err)
		}
		value.Entries = append(value.Entries, item)
		if item.Completed {
			value.Completed++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return domain.CatalogImportReport{}, storageError(err)
	}
	if len(value.Entries) != value.Total {
		return domain.CatalogImportReport{}, domain.ErrDatabase
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.CatalogImportReport{}, err
	}
	return value, storageError(tx.Commit(ctx))
}
