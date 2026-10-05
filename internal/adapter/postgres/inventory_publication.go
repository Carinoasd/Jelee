package postgres

import (
	"context"
	"errors"
	"math"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

type inventoryPreparation struct {
	mode, historyRoot, historyPath                     string
	excluded, excludedCopied                           int64
	snapshot, previous, revision, epoch, files, copied int64
	cursor                                             *string
	cleaned, ready                                     bool
}

func readInventoryPreparation(ctx context.Context, tx pgx.Tx, job string) (inventoryPreparation, error) {
	var p inventoryPreparation
	err := tx.QueryRow(ctx, `SELECT snapshot_id,previous_snapshot,baseline_revision,inventory_generation,source_files,copied,cursor_id::text,cleaned,ready,publication_mode,excluded_files,excluded_copied,COALESCE(excluded_after_root::text,''),excluded_after_path FROM inventory_snapshot_preparations WHERE job_id=$1::uuid`, job).Scan(&p.snapshot, &p.previous, &p.revision, &p.epoch, &p.files, &p.copied, &p.cursor, &p.cleaned, &p.ready, &p.mode, &p.excluded, &p.excludedCopied, &p.historyRoot, &p.historyPath)
	return p, storageError(err)
}

func checkInventoryPreparation(ctx context.Context, tx pgx.Tx, l domain.JobLease, p inventoryPreparation) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT active_inventory_snapshot=$2 AND inventory_baseline_revision=$3 AND inventory_generation=$4 FROM libraries WHERE id=$1::uuid`, l.Job.LibraryID, p.previous, p.revision, p.epoch).Scan(&valid)
	if err != nil {
		return storageError(err)
	}
	if !valid || l.Job.Files != p.files {
		return domain.ErrInventoryInvalidated
	}
	return nil
}

// PrepareInventoryPublication keeps each database transaction bounded. Staged
// rows are invisible through the baseline view until FinishJob switches the
// library pointer. A replacement lease can resume the durable keyset cursor.
func (s *Store) PrepareInventoryPublication(ctx context.Context, l domain.JobLease) (bool, error) {
	if ctx == nil || !validLease(l) {
		return false, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return false, err
	}
	if current.Job.CancelRequested {
		return false, context.Canceled
	}
	if current.Job.Kind != "inventory_scan" {
		return false, domain.ErrInvalid
	}
	var mode string
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT mode FROM job_ignore_requests WHERE job_id=$1::uuid),'')`, l.Job.ID).Scan(&mode); err != nil {
		return false, storageError(err)
	}
	if mode != "" {
		return prepareIgnoredInventory(ctx, tx, current, mode)
	}
	frozen, epoch, err := inventoryEpoch(ctx, tx, current)
	if err != nil {
		return false, err
	}
	if frozen != nil && *frozen != epoch {
		return false, domain.ErrInventoryInvalidated
	}
	p, err := readInventoryPreparation(ctx, tx, l.Job.ID)
	if errors.Is(err, domain.ErrNotFound) {
		covered, skipped, coverageErr := inventoryCoverage(ctx, tx, current)
		if coverageErr != nil {
			return false, coverageErr
		}
		if !covered {
			return false, domain.ErrConflict
		}
		if skipped || current.Job.Skipped != 0 || frozen == nil {
			return true, nil
		}
		var baseline, unknown, missing int64
		if err = tx.QueryRow(ctx, inventoryMissingCountsSQL, current.Job.LibraryID, l.Job.ID, epoch).Scan(&baseline, &unknown, &missing); err != nil {
			return false, storageError(err)
		}
		if baseline > 500000 {
			return false, domain.ErrScanLimit
		}
		if unknown > 0 {
			missing = 0
		}
		if missing > 0 && (missing >= int64(current.Policy.MissingCountLimit) || missing*100 >= baseline*int64(current.Policy.MissingPercentLimit)) {
			return true, nil
		}
		if _, err = tx.Exec(ctx, `INSERT INTO inventory_snapshot_preparations(job_id,library_id,previous_snapshot,baseline_revision,inventory_generation,source_files) SELECT $1::uuid,id,active_inventory_snapshot,inventory_baseline_revision,inventory_generation,$3 FROM libraries WHERE id=$2::uuid`, l.Job.ID, current.Job.LibraryID, current.Job.Files); err != nil {
			return false, storageError(err)
		}
		p, err = readInventoryPreparation(ctx, tx, l.Job.ID)
	}
	if err != nil {
		return false, err
	}
	if p.mode != "" {
		return false, domain.ErrConflict
	}
	return prepareInventorySnapshotBatch(ctx, tx, current, p)
}

func prepareInventorySnapshotBatch(ctx context.Context, tx pgx.Tx, current domain.JobLease, p inventoryPreparation) (bool, error) {
	if err := checkInventoryPreparation(ctx, tx, current, p); err != nil {
		return false, err
	}
	if p.revision == math.MaxInt64 {
		return false, domain.ErrScanLimit
	}
	if !p.ready && !p.cleaned {
		tag, err := tx.Exec(ctx, `WITH stale AS (SELECT ctid FROM library_inventory_baseline_data WHERE library_id=$1::uuid AND snapshot_id<>$2 AND snapshot_id<>$3 LIMIT 512) DELETE FROM library_inventory_baseline_data WHERE ctid IN (SELECT ctid FROM stale)`, current.Job.LibraryID, p.previous, p.snapshot)
		if err != nil {
			return false, storageError(err)
		}
		if tag.RowsAffected() > 0 {
			return finishInventoryPreparationBatch(ctx, tx, current, p, false)
		}
		p.cleaned = true
	}
	if !p.ready {
		copiedCurrent := false
		if p.copied < p.files {
			var count int64
			var cursor *string
			err := tx.QueryRow(ctx, `WITH entries AS MATERIALIZED (SELECT id,root_id,path,kind,size,modified_unix_nano FROM job_inventory WHERE job_id=$1::uuid AND id>COALESCE($2::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY id LIMIT 128), inserted AS (INSERT INTO library_inventory_baseline_data(library_id,snapshot_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision) SELECT $3::uuid,$4,root_id,path,true,kind,size,modified_unix_nano,$5,$6 FROM entries RETURNING 1) SELECT count(*),(SELECT id::text FROM entries ORDER BY id DESC LIMIT 1) FROM inserted`, current.Job.ID, p.cursor, current.Job.LibraryID, p.snapshot, p.epoch, p.revision+1).Scan(&count, &cursor)
			if err != nil {
				return false, storageError(err)
			}
			p.copied += count
			if count == 0 || p.copied > p.files {
				return false, domain.ErrInventoryInvalidated
			}
			p.cursor = cursor
			copiedCurrent = true
		}
		if p.copied == p.files {
			var more bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_inventory WHERE job_id=$1::uuid AND id>COALESCE($2::uuid,'00000000-0000-0000-0000-000000000000'::uuid))`, current.Job.ID, p.cursor).Scan(&more); err != nil {
				return false, storageError(err)
			}
			if more {
				return false, domain.ErrInventoryInvalidated
			}
			if !copiedCurrent && p.excludedCopied < p.excluded {
				if err := copyIgnoredHistory(ctx, tx, current, &p); err != nil {
					return false, err
				}
			}
			p.ready = p.excludedCopied == p.excluded
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory_snapshot_preparations SET copied=$2,cursor_id=$3::uuid,cleaned=true,ready=$4,excluded_copied=$5,excluded_after_root=NULLIF($6,'')::uuid,excluded_after_path=$7 WHERE job_id=$1::uuid`, current.Job.ID, p.copied, p.cursor, p.ready, p.excludedCopied, p.historyRoot, p.historyPath); err != nil {
			return false, storageError(err)
		}
	}
	return finishInventoryPreparationBatch(ctx, tx, current, p, p.ready)
}

func finishInventoryPreparationBatch(ctx context.Context, tx pgx.Tx, l domain.JobLease, p inventoryPreparation, ready bool) (bool, error) {
	if err := guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return false, err
	}
	if err := guardInventoryFinish(ctx, tx, l, &p.epoch, true); err != nil {
		return false, err
	}
	return ready, storageError(tx.Commit(ctx))
}

func publishPreparedInventory(ctx context.Context, tx pgx.Tx, l domain.JobLease, mode string, excluded int64) (bool, error) {
	p, err := readInventoryPreparation(ctx, tx, l.Job.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = checkInventoryPreparation(ctx, tx, l, p); err != nil {
		return false, err
	}
	if p.mode != mode || p.excluded != excluded {
		return false, domain.ErrInventoryInvalidated
	}
	if !p.ready || p.copied != p.files || p.excludedCopied != p.excluded {
		return false, domain.ErrConflict
	}
	var rows int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM library_inventory_baseline_data WHERE library_id=$1::uuid AND snapshot_id=$2`, l.Job.LibraryID, p.snapshot).Scan(&rows); err != nil {
		return false, storageError(err)
	}
	if rows != p.files+p.excluded {
		return false, domain.ErrInventoryInvalidated
	}
	_, err = tx.Exec(ctx, `UPDATE libraries SET active_inventory_snapshot=$2,inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid`, l.Job.LibraryID, p.snapshot)
	return true, storageError(err)
}
