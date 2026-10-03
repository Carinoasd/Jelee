package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// This private first record is unresolved. It cannot authorize a filesystem
// operation or certify replacement/rollback; native recovery is still required.
type nfoWriteCommitRecord = domain.NFOWriteCommitRecord

const nfoWriteCommitColumns = `job_id::text,sequence,generation,token::text,owner,recorded_at,lease_until`

func scanNFOWriteCommitRecord(row pgx.Row) (nfoWriteCommitRecord, error) {
	var r nfoWriteCommitRecord
	err := row.Scan(&r.JobID, &r.Sequence, &r.Generation, &r.Token, &r.Owner, &r.RecordedAt, &r.LeaseUntil)
	return r, err
}

// The caller owns the short job transaction and must commit before returning
// the record. No production worker invokes this step until scope authorization,
// native staging identity, the commit boundary and resolution are connected.
func recordNFOWriteCommit(ctx context.Context, tx pgx.Tx, lease domain.JobLease, sequence int) (nfoWriteCommitRecord, error) {
	if ctx == nil || sequence < 1 || sequence > 100 {
		return nfoWriteCommitRecord{}, domain.ErrInvalid
	}
	current, err := fencedNFOCommitLease(ctx, tx, lease)
	if err != nil {
		return nfoWriteCommitRecord{}, err
	}
	if current.Job.Kind != domain.JobNFOWrite {
		return nfoWriteCommitRecord{}, domain.ErrInvalid
	}
	// A recovery lease exists to settle stopped work, including cancelled jobs.
	if current.Job.CancelRequested && lease.RecoveryEpoch == 0 {
		return nfoWriteCommitRecord{}, context.Canceled
	}
	var actor string
	if err := tx.QueryRow(ctx, `SELECT u.id::text FROM jobs j JOIN users u ON u.id=j.actor_id WHERE j.id=$1::uuid AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF u`, lease.Job.ID).Scan(&actor); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nfoWriteCommitRecord{}, domain.ErrForbidden
		}
		return nfoWriteCommitRecord{}, storageError(err)
	}
	r, err := scanNFOWriteCommitRecord(tx.QueryRow(ctx, `SELECT `+nfoWriteCommitColumns+` FROM nfo_write_commit_journal WHERE job_id=$1::uuid AND sequence=$2 AND generation=$3`, lease.Job.ID, sequence, current.Generation))
	if errors.Is(err, pgx.ErrNoRows) && lease.RecoveryEpoch == 0 {
		r, err = scanNFOWriteCommitRecord(tx.QueryRow(ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,$2,$3,$4) RETURNING `+nfoWriteCommitColumns, lease.Job.ID, sequence, lease.Generation, lease.Owner))
	}
	if err != nil {
		return nfoWriteCommitRecord{}, storageError(err)
	}
	if lease.RecoveryEpoch != 0 {
		// Recovery continues the existing token and never opens a new journal.
		if _, err := tx.Exec(ctx, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, lease.Job.ID); err != nil {
			return nfoWriteCommitRecord{}, storageError(err)
		}
		return r, nil
	}
	if r.Owner != lease.Owner {
		return nfoWriteCommitRecord{}, domain.ErrJobLeaseLost
	}
	if err := guardedJobUpdate(ctx, tx, lease, `UPDATE jobs SET generation=generation WHERE id=$1::uuid AND NOT cancel_requested`, lease.Job.ID); err != nil {
		return nfoWriteCommitRecord{}, err
	}
	return r, nil
}
