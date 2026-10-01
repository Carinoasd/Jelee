package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Schema 007's unknown attributes also lack a source epoch. Neither general
// nor image missing counts may infer absence from that historical scope.
const baselineUnknownScopeSQL = `(NOT b.attributes_known OR b.inventory_generation IS DISTINCT FROM $3)`

const inventoryMissingCountsSQL = `SELECT count(*),count(*) FILTER(WHERE ` + baselineUnknownScopeSQL + `),count(*) FILTER(WHERE i.id IS NULL)
 FROM library_inventory_baseline b LEFT JOIN job_inventory i ON i.job_id=$2::uuid AND i.root_id=b.root_id AND i.path=b.path WHERE b.library_id=$1::uuid`

// A drained frontier is not sufficient: every configured root must have its
// completed root record, and no foreign root may contribute an observation.
// The caller holds the library row lock through the final epoch/lease guard.
func inventoryCoverage(ctx context.Context, tx pgx.Tx, l domain.JobLease) (bool, bool, error) {
	var complete, skipped bool
	err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM library_roots WHERE library_id=$2::uuid)
 AND NOT EXISTS(SELECT 1 FROM library_roots r WHERE r.library_id=$2::uuid AND NOT EXISTS(
  SELECT 1 FROM job_directories d WHERE d.job_id=$1::uuid AND d.root_id=r.id AND d.path='.' AND d.parent_path IS NULL AND d.done))
 AND NOT EXISTS(SELECT 1 FROM job_directories d JOIN library_roots r ON r.id=d.root_id WHERE d.job_id=$1::uuid AND
  (NOT d.done OR r.library_id<>$2::uuid OR (d.path='.' AND d.parent_path IS NOT NULL)))
 AND NOT EXISTS(SELECT 1 FROM job_inventory i JOIN library_roots r ON r.id=i.root_id WHERE i.job_id=$1::uuid AND r.library_id<>$2::uuid),
 EXISTS(SELECT 1 FROM job_directories WHERE job_id=$1::uuid AND skipped>0)`, l.Job.ID, l.Job.LibraryID).Scan(&complete, &skipped)
	return complete, skipped, storageError(err)
}
