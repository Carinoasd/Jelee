package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// AcquireNFOWriteCommitRecovery takes the recovery lease of a stopped NFO write
// job that still holds a commit journal. A live lease held by anyone is never
// replaced; an expired one passes to the new owner with the next epoch. The
// lease continues the job's existing tokens only. It never starts a new journal,
// changes the job or grants filesystem authority by itself.
func (s *Store) AcquireNFOWriteCommitRecovery(ctx context.Context, jobID, owner string, ttl time.Duration) (domain.JobLease, error) {
	if ctx == nil || !domain.ValidID(jobID) || !validText(owner, 128, false) || !validLeaseDuration(ttl) {
		return domain.JobLease{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.JobLease{}, err
	}
	defer tx.Rollback(ctx)
	var epoch int64
	err = tx.QueryRow(ctx, `INSERT INTO nfo_write_commit_recovery_leases(job_id,owner,epoch,recorded_at,lease_until) VALUES($1::uuid,$2,1,clock_timestamp(),clock_timestamp()+$3*interval '1 millisecond')
 ON CONFLICT(job_id) DO UPDATE SET owner=EXCLUDED.owner,epoch=nfo_write_commit_recovery_leases.epoch+1,lease_until=EXCLUDED.lease_until
 WHERE nfo_write_commit_recovery_leases.lease_until<=clock_timestamp() RETURNING epoch`, jobID, owner, ttl.Milliseconds()).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.JobLease{}, domain.ErrConflict
	}
	if err != nil {
		return domain.JobLease{}, storageError(err)
	}
	lease, err := fencedNFOCommitRecovery(ctx, tx, domain.JobLease{Job: domain.Job{ID: jobID}, Owner: owner, RecoveryEpoch: epoch})
	if err != nil {
		return domain.JobLease{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.JobLease{}, storageError(err)
	}
	return lease, nil
}

// RenewNFOWriteCommitRecovery extends a recovery lease that is still held.
func (s *Store) RenewNFOWriteCommitRecovery(ctx context.Context, lease domain.JobLease, ttl time.Duration) (domain.JobLease, error) {
	if ctx == nil || lease.RecoveryEpoch < 1 || !validLease(lease) || !validLeaseDuration(ttl) {
		return domain.JobLease{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.JobLease{}, err
	}
	defer tx.Rollback(ctx)
	// Lock the job before the lease row, the order every NFO commit writer uses.
	if _, err = tx.Exec(ctx, `SELECT 1 FROM jobs WHERE id=$1::uuid FOR UPDATE`, lease.Job.ID); err != nil {
		return domain.JobLease{}, storageError(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE nfo_write_commit_recovery_leases SET lease_until=clock_timestamp()+$4*interval '1 millisecond' WHERE job_id=$1::uuid AND owner=$2 AND epoch=$3 AND lease_until>clock_timestamp()`, lease.Job.ID, lease.Owner, lease.RecoveryEpoch, ttl.Milliseconds())
	if err != nil {
		return domain.JobLease{}, storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.JobLease{}, domain.ErrJobLeaseLost
	}
	renewed, err := fencedNFOCommitRecovery(ctx, tx, lease)
	if err != nil {
		return domain.JobLease{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.JobLease{}, storageError(err)
	}
	return renewed, nil
}

// fencedNFOCommitLease locks the job under either its original lease or the
// caller's recovery lease. Every NFO commit transaction uses this fence.
func fencedNFOCommitLease(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, error) {
	if l.RecoveryEpoch == 0 {
		return fencedJob(ctx, tx, l)
	}
	return fencedNFOCommitRecovery(ctx, tx, l)
}

func fencedNFOCommitRecovery(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, error) {
	if l.RecoveryEpoch < 1 || !domain.ValidID(l.Job.ID) || !validText(l.Owner, 128, false) {
		return domain.JobLease{}, domain.ErrJobLeaseLost
	}
	current := domain.JobLease{Owner: l.Owner, RecoveryEpoch: l.RecoveryEpoch}
	err := tx.QueryRow(ctx, `SELECT `+jobColumns+`,generation FROM jobs WHERE id=$1::uuid AND kind='nfo_write' AND state IN ('failed','cancelled') AND owner IS NULL FOR UPDATE`, l.Job.ID).Scan(append(jobScanTargets(&current.Job), &current.Generation)...)
	if err == nil && l.Generation != 0 && l.Generation != current.Generation {
		err = pgx.ErrNoRows
	}
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT lease_until FROM nfo_write_commit_recovery_leases WHERE job_id=$1::uuid AND owner=$2 AND epoch=$3 AND lease_until>clock_timestamp() FOR SHARE`, l.Job.ID, l.Owner, l.RecoveryEpoch).Scan(&current.ExpiresAt)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.JobLease{}, domain.ErrJobLeaseLost
	}
	if err != nil {
		return domain.JobLease{}, storageError(err)
	}
	return current, nil
}

// nfoCommitLeaseLive repeats the fence at the end of a read transaction.
func nfoCommitLeaseLive(ctx context.Context, tx pgx.Tx, l domain.JobLease) error {
	var live bool
	var err error
	if l.RecoveryEpoch == 0 {
		err = tx.QueryRow(ctx, `SELECT state='running' AND owner=$2 AND generation=$3 AND lease_until>clock_timestamp() AND NOT cancel_requested FROM jobs WHERE id=$1::uuid`, l.Job.ID, l.Owner, l.Generation).Scan(&live)
	} else {
		err = tx.QueryRow(ctx, `SELECT j.state IN ('failed','cancelled') AND j.owner IS NULL AND j.generation=$4 FROM jobs j JOIN nfo_write_commit_recovery_leases r ON r.job_id=j.id WHERE j.id=$1::uuid AND r.owner=$2 AND r.epoch=$3 AND r.lease_until>clock_timestamp()`, l.Job.ID, l.Owner, l.RecoveryEpoch, l.Generation).Scan(&live)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrJobLeaseLost
	}
	if err != nil {
		return storageError(err)
	}
	if !live {
		return domain.ErrJobLeaseLost
	}
	return nil
}
