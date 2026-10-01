package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) BeginFamilyIgnoreBaselineComparison(ctx context.Context, l domain.JobLease) error {
	return s.beginIgnoreBaselineComparison(ctx, l, true)
}

func (s *Store) NextFamilyIgnoreBaselinePage(ctx context.Context, l domain.JobLease) (domain.IgnoreBaselinePage, error) {
	return s.nextIgnoreBaselinePage(ctx, l, true)
}

func comparisonModeFence(ctx context.Context, tx pgx.Tx, l domain.JobLease, family bool) (domain.JobLease, int64, int64, error) {
	if !family {
		return ignoreComparisonFence(ctx, tx, l)
	}
	current, epoch, err := legacyManifestFence(ctx, tx, l)
	if err != nil {
		return current, 0, 0, err
	}
	var valid bool
	var revision int64
	err = tx.QueryRow(ctx, `SELECT NOT c.invalidated AND NOT m.invalidated AND c.inventory_generation=$2 AND m.inventory_generation=$2,l.inventory_baseline_revision FROM job_ignore_manifests c JOIN job_ignore_legacy_manifests m ON m.job_id=c.job_id JOIN jobs j ON j.id=c.job_id JOIN libraries l ON l.id=j.library_id WHERE c.job_id=$1::uuid`, l.Job.ID, epoch).Scan(&valid, &revision)
	if err != nil {
		return current, 0, 0, storageError(err)
	}
	if !valid {
		return current, 0, 0, domain.ErrInventoryInvalidated
	}
	return current, epoch, revision, nil
}

func familyBaselineRootCoverage(ctx context.Context, tx pgx.Tx, l domain.JobLease) error {
	var covered bool
	// A shadowed root proof from a deeper lookup is insufficient. Each root
	// must have its own query and agree with the custom directory identity.
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM library_roots WHERE library_id=$2::uuid) AND NOT EXISTS(
 SELECT 1 FROM library_roots r WHERE r.library_id=$2::uuid AND NOT EXISTS(
 SELECT 1 FROM job_ignore_legacy_queries q JOIN job_ignore_legacy_proofs p ON p.job_id=q.job_id AND p.root_id=q.root_id AND p.directory='.'
 JOIN job_ignore_proofs c ON c.job_id=p.job_id AND c.root_id=p.root_id AND c.directory='.'
 WHERE q.job_id=$1::uuid AND q.root_id=r.id AND q.directory='.' AND p.checked AND NOT c.missing_directory AND p.identity=c.identity AND p.parent_identity=c.parent_identity))`, l.Job.ID, l.Job.LibraryID).Scan(&covered)
	if err != nil {
		return storageError(err)
	}
	if !covered {
		return domain.ErrConflict
	}
	return nil
}
