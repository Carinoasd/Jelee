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

const imageCurrentCountsSQL = `SELECT
 count(*) FILTER(WHERE $3::bigint IS NOT NULL AND (b.root_id IS NULL OR b.attributes_known AND b.inventory_generation=$3 AND b.kind<>'image')),
 count(*) FILTER(WHERE $3::bigint IS NOT NULL AND b.attributes_known AND b.inventory_generation=$3 AND b.kind='image' AND (b.size<>i.size OR b.modified_unix_nano<>i.modified_unix_nano)),
 count(*) FILTER(WHERE $3::bigint IS NOT NULL AND b.attributes_known AND b.inventory_generation=$3 AND b.kind='image' AND b.size=i.size AND b.modified_unix_nano=i.modified_unix_nano),
 count(*) FILTER(WHERE $3::bigint IS NULL OR b.root_id IS NOT NULL AND (NOT b.attributes_known OR b.inventory_generation IS DISTINCT FROM $3))
 FROM job_inventory i LEFT JOIN library_inventory_baseline b ON b.library_id=$1::uuid AND b.root_id=i.root_id AND b.path=i.path WHERE i.job_id=$2::uuid AND i.kind='image'`
const imageMissingCountsSQL = `SELECT count(*),count(*) FILTER(WHERE NOT b.attributes_known OR b.inventory_generation IS DISTINCT FROM $3),count(*) FILTER(WHERE b.attributes_known AND b.inventory_generation=$3 AND b.kind='image' AND (i.id IS NULL OR i.kind<>'image')) FROM library_inventory_baseline b LEFT JOIN job_inventory i ON i.job_id=$2::uuid AND i.root_id=b.root_id AND i.path=b.path WHERE b.library_id=$1::uuid`

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
