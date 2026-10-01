package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.IgnoreExecutionRepository = (*Store)(nil)

func (s *Store) ReadIgnoreRequest(ctx context.Context, l domain.JobLease) (*domain.IgnoreRequest, error) {
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
	request, err := loadIgnoreRequest(ctx, tx, l.Job.ID)
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
	if !domain.ValidID(rootID) {
		return "", domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := ignoreManifestFence(ctx, tx, l)
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
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.IgnoreExecutionProgress{}, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := ignoreManifestFence(ctx, tx, l)
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
