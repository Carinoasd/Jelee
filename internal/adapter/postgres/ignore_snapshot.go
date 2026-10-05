package postgres

import (
	"context"
	"errors"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Preparation happens before source verification. Only the final publisher may
// accept a freshly verified seal and make these derived rows visible.
func prepareIgnoredInventory(ctx context.Context, tx pgx.Tx, l domain.JobLease, mode string) (bool, error) {
	if mode != domain.IgnoreModeJeleeignore && mode != domain.IgnoreModeFamily {
		return false, domain.ErrIgnoreUnavailable
	}
	current, epoch, revision, err := comparisonModeFence(ctx, tx, l, mode == domain.IgnoreModeFamily)
	if err != nil {
		return false, err
	}
	c, err := loadIgnoreComparison(ctx, tx, l.Job.ID)
	if err != nil {
		return false, err
	}
	if c.epoch != epoch || c.token.BaselineRevision != revision {
		return false, domain.ErrInventoryInvalidated
	}
	if !c.complete {
		return false, domain.ErrConflict
	}
	covered, skipped, err := inventoryCoverage(ctx, tx, current)
	if err != nil {
		return false, err
	}
	if !covered || skipped || current.Job.Skipped != 0 {
		return false, domain.ErrConflict
	}
	result, err := domain.CompareIgnoreBaseline(c.counts, true, c.comparable, current.Policy.MissingCountLimit, current.Policy.MissingPercentLimit)
	if err != nil {
		return false, err
	}
	if result.ReviewRequired {
		return true, nil
	}
	excluded := int64(0)
	if c.comparable {
		excluded = c.counts.Excluded
	}
	if current.Job.Files > 500000-excluded || current.Job.Files+excluded > int64(current.Policy.MaxEntries) {
		return false, domain.ErrScanLimit
	}
	p, err := readInventoryPreparation(ctx, tx, l.Job.ID)
	if errors.Is(err, domain.ErrNotFound) {
		if _, err = tx.Exec(ctx, `INSERT INTO inventory_snapshot_preparations(job_id,library_id,previous_snapshot,baseline_revision,inventory_generation,source_files,publication_mode,excluded_files) SELECT $1::uuid,id,active_inventory_snapshot,inventory_baseline_revision,inventory_generation,$3,$4,$5 FROM libraries WHERE id=$2::uuid`, l.Job.ID, current.Job.LibraryID, current.Job.Files, mode, excluded); err != nil {
			return false, storageError(err)
		}
		p, err = readInventoryPreparation(ctx, tx, l.Job.ID)
	}
	if err != nil {
		return false, err
	}
	if p.mode != mode || p.excluded != excluded {
		return false, domain.ErrInventoryInvalidated
	}
	return prepareInventorySnapshotBatch(ctx, tx, current, p)
}

func copyIgnoredHistory(ctx context.Context, tx pgx.Tx, l domain.JobLease, p *inventoryPreparation) error {
	if p.mode != domain.IgnoreModeJeleeignore && p.mode != domain.IgnoreModeFamily {
		return domain.ErrConflict
	}
	// Read a bounded raw decision page before filtering exclusions. Sparse
	// exclusions cannot force one batch to search the whole remaining suffix.
	query := `WITH raw AS MATERIALIZED (
 SELECT root_id,path,outcome FROM ` + ignoreDecisionTable(p.mode == domain.IgnoreModeFamily) + `
 WHERE job_id=$1::uuid AND (root_id,path)>(COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid),$3 COLLATE "C")
 ORDER BY root_id,path LIMIT 128), inserted AS (
 INSERT INTO library_inventory_baseline_data(library_id,snapshot_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision)
 SELECT b.library_id,$5,b.root_id,b.path,b.attributes_known,b.kind,b.size,b.modified_unix_nano,b.inventory_generation,b.observed_revision
 FROM raw r JOIN library_inventory_baseline_data b ON b.library_id=$4::uuid AND b.snapshot_id=$6 AND b.root_id=r.root_id AND b.path COLLATE "C"=r.path
 WHERE r.outcome='excluded' RETURNING 1)
 SELECT (SELECT count(*) FROM raw),(SELECT count(*) FROM raw WHERE outcome='excluded'),count(*),
 (SELECT root_id::text FROM raw ORDER BY raw.root_id DESC,raw.path DESC LIMIT 1),
 (SELECT path FROM raw ORDER BY raw.root_id DESC,raw.path DESC LIMIT 1) FROM inserted`
	var raw, expected, copied int64
	var root, path *string
	if err := tx.QueryRow(ctx, query, l.Job.ID, p.historyRoot, p.historyPath, l.Job.LibraryID, p.snapshot, p.previous).Scan(&raw, &expected, &copied, &root, &path); err != nil {
		return storageError(err)
	}
	if raw == 0 || root == nil || path == nil || copied != expected || p.excludedCopied+copied > p.excluded {
		return domain.ErrInventoryInvalidated
	}
	p.excludedCopied += copied
	p.historyRoot, p.historyPath = *root, *path
	return nil
}
