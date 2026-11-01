package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var (
	_ app.JobCancellationReader = (*Store)(nil)
	_ app.JobProgressReader     = (*Store)(nil)
)

// A single primary-key lookup reads only committed state. It does not acquire
// the scheduler lock, update a row, or extend job/probe leases. Publication
// methods still independently fence the owner and generation in transactions.
func (s *Store) ReadJobCancellation(ctx context.Context, lease domain.JobLease) (bool, error) {
	if ctx == nil {
		return false, domain.ErrInvalid
	}
	if !validLease(lease) {
		return false, domain.ErrJobLeaseLost
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var requested bool
	err := s.Pool.QueryRow(bounded, `SELECT cancel_requested FROM jobs WHERE id=$1::uuid AND state='running' AND owner=$2 AND generation=$3 AND lease_until>clock_timestamp()`, lease.Job.ID, lease.Owner, lease.Generation).Scan(&requested)
	err = storageError(err)
	if errors.Is(err, domain.ErrNotFound) {
		err = domain.ErrJobLeaseLost
	}
	return requested, err
}

// ReadJobProgress reads the public progress of a claimed job (G46.3) with
// the same single fenced primary-key lookup as ReadJobCancellation.
func (s *Store) ReadJobProgress(ctx context.Context, lease domain.JobLease) (domain.JobProgress, error) {
	if ctx == nil {
		return domain.JobProgress{}, domain.ErrInvalid
	}
	if !validLease(lease) {
		return domain.JobProgress{}, domain.ErrJobLeaseLost
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	j, err := scanJob(s.Pool.QueryRow(bounded, `SELECT `+jobColumns+` FROM jobs WHERE id=$1::uuid AND state='running' AND owner=$2 AND generation=$3 AND lease_until>clock_timestamp()`, lease.Job.ID, lease.Owner, lease.Generation))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.JobProgress{}, domain.ErrJobLeaseLost
	}
	if err != nil {
		return domain.JobProgress{}, err
	}
	// scanJob always projects the progress.
	return *j.Progress, nil
}
