package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.ImageQueryRepository = (*Store)(nil)

func inventoryEpoch(ctx context.Context, tx pgx.Tx, l domain.JobLease) (*int64, int64, error) {
	var frozen *int64
	var current int64
	err := tx.QueryRow(ctx, `SELECT j.inventory_generation,b.inventory_generation FROM jobs j JOIN libraries b ON b.id=j.library_id WHERE j.id=$1::uuid FOR UPDATE OF b`, l.Job.ID).Scan(&frozen, &current)
	return frozen, current, storageError(err)
}

const imageCurrentCountsSQL = `WITH current AS MATERIALIZED (
 SELECT root_id,path,size,modified_unix_nano FROM job_inventory WHERE job_id=$2::uuid AND kind='image'),
 observations AS (
 SELECT root_id,path,true AS observed,true AS attributes_known,$3::bigint AS inventory_generation,'image'::text AS kind,size,modified_unix_nano FROM current
 UNION ALL
 SELECT root_id,path,false,attributes_known,inventory_generation,kind,size,modified_unix_nano FROM library_inventory_baseline_data
 WHERE library_id=$1::uuid AND snapshot_id=(SELECT active_inventory_snapshot FROM libraries WHERE id=$1::uuid) AND EXISTS(SELECT 1 FROM current)),
 compared AS (
 SELECT root_id,path,bool_or(observed) AS observed,bool_or(NOT observed) AS has_baseline,
 bool_or(attributes_known) FILTER(WHERE NOT observed) AS known,max(inventory_generation) FILTER(WHERE NOT observed) AS epoch,
 max(kind) FILTER(WHERE NOT observed) AS kind,max(size) FILTER(WHERE observed) AS current_size,max(size) FILTER(WHERE NOT observed) AS baseline_size,
 max(modified_unix_nano) FILTER(WHERE observed) AS current_mtime,max(modified_unix_nano) FILTER(WHERE NOT observed) AS baseline_mtime
 FROM observations GROUP BY root_id,path)
 SELECT count(*) FILTER(WHERE $3::bigint IS NOT NULL AND (NOT has_baseline OR known AND epoch=$3 AND kind<>'image')),
 count(*) FILTER(WHERE $3::bigint IS NOT NULL AND known AND epoch=$3 AND kind='image' AND (baseline_size<>current_size OR baseline_mtime<>current_mtime)),
 count(*) FILTER(WHERE $3::bigint IS NOT NULL AND known AND epoch=$3 AND kind='image' AND baseline_size=current_size AND baseline_mtime=current_mtime),
 count(*) FILTER(WHERE $3::bigint IS NULL OR has_baseline AND (NOT known OR epoch IS DISTINCT FROM $3)) FROM compared WHERE observed`
const imageMissingCountsSQL = `WITH baseline AS MATERIALIZED (
 SELECT root_id,path,attributes_known,inventory_generation,kind FROM library_inventory_baseline_data
 WHERE library_id=$1::uuid AND snapshot_id=(SELECT active_inventory_snapshot FROM libraries WHERE id=$1::uuid)),
 missing AS (SELECT root_id,path FROM baseline WHERE attributes_known AND inventory_generation=$3 AND kind='image'
 EXCEPT SELECT root_id,path FROM job_inventory WHERE job_id=$2::uuid AND kind='image')
 SELECT count(*),count(*) FILTER(WHERE ` + baselineUnknownScopeSQL + `),(SELECT count(*) FROM missing) FROM baseline b`

func saveImageProgress(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch *int64, complete bool) error {
	var p domain.ImageProgress
	var baseline, unknown, missing int64
	if err := tx.QueryRow(ctx, imageCurrentCountsSQL, l.Job.LibraryID, l.Job.ID, epoch).Scan(&p.Added, &p.Changed, &p.Unchanged, &p.Uncompared); err != nil {
		return storageError(err)
	}
	if err := tx.QueryRow(ctx, imageMissingCountsSQL, l.Job.LibraryID, l.Job.ID, epoch).Scan(&baseline, &unknown, &missing); err != nil {
		return storageError(err)
	}
	if baseline > 500000 {
		return domain.ErrScanLimit
	}
	p.ComparisonComplete = complete && epoch != nil && unknown == 0 && p.Uncompared == 0
	if p.ComparisonComplete {
		p.Missing = missing
	}
	if err := domain.ValidateImageProgress(p); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO image_job_state(job_id,library_id,added,changed,unchanged,missing,uncompared,comparison_complete) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8) ON CONFLICT(job_id) DO UPDATE SET added=EXCLUDED.added,changed=EXCLUDED.changed,unchanged=EXCLUDED.unchanged,missing=EXCLUDED.missing,uncompared=EXCLUDED.uncompared,comparison_complete=EXCLUDED.comparison_complete`, l.Job.ID, l.Job.LibraryID, p.Added, p.Changed, p.Unchanged, p.Missing, p.Uncompared, p.ComparisonComplete)
	return storageError(err)
}
func (s *Store) GetImageJobSummary(parent context.Context, a domain.Actor, id string) (domain.ImageJobSummary, error) {
	if parent == nil {
		return domain.ImageJobSummary{}, domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return domain.ImageJobSummary{}, domain.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.ImageJobSummary{}, err
	}
	defer tx.Rollback(ctx)
	var result domain.ImageJobSummary
	var compared bool
	err = tx.QueryRow(ctx, `SELECT j.id::text,j.library_id::text,COALESCE(p.added,0),COALESCE(p.changed,0),COALESCE(p.unchanged,0),COALESCE(p.missing,0),COALESCE(p.uncompared,0),COALESCE(p.comparison_complete,false),p.job_id IS NOT NULL FROM jobs j LEFT JOIN image_job_state p ON p.job_id=j.id WHERE j.id=$1::uuid`, id).Scan(&result.JobID, &result.LibraryID, &result.Added, &result.Changed, &result.Unchanged, &result.Missing, &result.Uncompared, &result.ComparisonComplete, &compared)
	if err != nil {
		return domain.ImageJobSummary{}, storageError(err)
	}
	if !compared {
		// Release/cancel/expired-lease recovery does no per-job comparison work.
		// Retain its observed image count without consulting a newer baseline:
		// later successful jobs must never change these historical statistics.
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM job_inventory WHERE job_id=$1::uuid AND kind='image'`, id).Scan(&result.Uncompared); err != nil {
			return domain.ImageJobSummary{}, storageError(err)
		}
	}
	if err = domain.ValidateImageJobSummary(result); err != nil {
		return domain.ImageJobSummary{}, domain.ErrDatabase
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.ImageJobSummary{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ImageJobSummary{}, storageError(err)
	}
	return result, nil
}

// After all terminal writes, verify the captured lease deadline and root epoch
// again. The library row is locked throughout comparison and publication.
func guardInventoryFinish(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch *int64, success bool) error {
	var live, unchanged bool
	err := tx.QueryRow(ctx, `SELECT j.generation=$2 AND $3::timestamptz>clock_timestamp(),($4::bigint IS NULL OR b.inventory_generation=$4) FROM jobs j JOIN libraries b ON b.id=j.library_id WHERE j.id=$1::uuid`, l.Job.ID, l.Generation, l.ExpiresAt, epoch).Scan(&live, &unchanged)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrJobLeaseLost
	}
	if err != nil {
		return storageError(err)
	}
	if !live {
		return domain.ErrJobLeaseLost
	}
	if success && !unchanged {
		return domain.ErrInventoryInvalidated
	}
	return nil
}
