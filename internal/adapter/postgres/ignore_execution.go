package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.IgnoreExecutionRepository = (*Store)(nil)
var _ app.FamilyIgnoreExecutionRepository = (*Store)(nil)

func (s *Store) ReadIgnoreRequest(ctx context.Context, l domain.JobLease) (*domain.IgnoreRequest, error) {
	return s.readExecutionIgnoreRequest(ctx, l, false)
}

// ReadExecutionIgnoreRequest identifies retained modes for explicit dispatch.
// Reading a request does not grant claim or public admission capability.
func (s *Store) ReadExecutionIgnoreRequest(ctx context.Context, l domain.JobLease) (*domain.IgnoreRequest, error) {
	return s.readExecutionIgnoreRequest(ctx, l, true)
}

func (s *Store) readExecutionIgnoreRequest(ctx context.Context, l domain.JobLease, familyAllowed bool) (*domain.IgnoreRequest, error) {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return nil, err
	}
	if current.Job.CancelRequested {
		return nil, context.Canceled
	}
	request, err := loadExecutionIgnoreRequest(ctx, tx, l.Job.ID, familyAllowed)
	if err != nil {
		return nil, err
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storageError(err)
	}
	return request, nil
}

func (s *Store) ReadIgnoreRoot(ctx context.Context, l domain.JobLease, rootID string) (string, error) {
	return s.readIgnoreRoot(ctx, l, rootID, false)
}

func (s *Store) ReadFamilyIgnoreRoot(ctx context.Context, l domain.JobLease, rootID string) (string, error) {
	return s.readIgnoreRoot(ctx, l, rootID, true)
}

func executionModeFence(ctx context.Context, tx pgx.Tx, l domain.JobLease, family bool) (domain.JobLease, int64, error) {
	if !family {
		return ignoreManifestFence(ctx, tx, l)
	}
	current, epoch, err := legacyManifestFence(ctx, tx, l)
	if err != nil {
		return current, 0, err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM job_ignore_manifests WHERE job_id=$1::uuid AND (invalidated OR inventory_generation<>$2))
 AND NOT EXISTS(SELECT 1 FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid AND (invalidated OR inventory_generation<>$2))
 AND NOT EXISTS(SELECT 1 FROM job_ignore_comparisons c JOIN jobs j ON j.id=c.job_id JOIN libraries b ON b.id=j.library_id WHERE c.job_id=$1::uuid AND (c.inventory_generation<>$2 OR c.baseline_revision<>b.inventory_baseline_revision))
 AND (NOT EXISTS(SELECT 1 FROM job_ignore_comparisons WHERE job_id=$1::uuid) OR
 (EXISTS(SELECT 1 FROM job_ignore_manifests WHERE job_id=$1::uuid) AND EXISTS(SELECT 1 FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid)))`, l.Job.ID, epoch).Scan(&valid)
	if err != nil {
		return current, 0, storageError(err)
	}
	if !valid {
		return current, 0, domain.ErrInventoryInvalidated
	}
	return current, epoch, nil
}

func (s *Store) readIgnoreRoot(ctx context.Context, l domain.JobLease, rootID string, family bool) (string, error) {
	if !domain.ValidID(rootID) {
		return "", domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := executionModeFence(ctx, tx, l, family)
	if err != nil {
		return "", err
	}
	var root string
	err = tx.QueryRow(ctx, `SELECT path FROM library_roots WHERE id=$1::uuid AND library_id=$2::uuid`, rootID, current.Job.LibraryID).Scan(&root)
	if err != nil {
		return "", storageError(err)
	}
	if err = commitIgnoreManifest(ctx, tx, current, epoch); err != nil {
		return "", err
	}
	return root, nil
}

func (s *Store) ReadIgnoreProgress(ctx context.Context, l domain.JobLease) (domain.IgnoreExecutionProgress, error) {
	return s.readIgnoreProgress(ctx, l, false)
}

func (s *Store) ReadFamilyIgnoreProgress(ctx context.Context, l domain.JobLease) (domain.IgnoreExecutionProgress, error) {
	return s.readIgnoreProgress(ctx, l, true)
}

func (s *Store) readIgnoreProgress(ctx context.Context, l domain.JobLease, family bool) (domain.IgnoreExecutionProgress, error) {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.IgnoreExecutionProgress{}, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := executionModeFence(ctx, tx, l, family)
	if err != nil {
		return domain.IgnoreExecutionProgress{}, err
	}
	var progress domain.IgnoreExecutionProgress
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_ignore_comparisons WHERE job_id=$1::uuid),EXISTS(SELECT 1 FROM job_ignore_comparisons WHERE job_id=$1::uuid AND unknown>0)`, l.Job.ID).Scan(&progress.ComparisonStarted, &progress.Unknown)
	if err != nil {
		return domain.IgnoreExecutionProgress{}, storageError(err)
	}
	if err = commitIgnoreManifest(ctx, tx, current, epoch); err != nil {
		return domain.IgnoreExecutionProgress{}, err
	}
	return progress, nil
}
