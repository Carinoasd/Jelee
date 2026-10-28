package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.NFOWriteCleanupRepository = (*Store)(nil)

// nfoCommitCleanupIdentityLimit bounds the earlier originals read for one
// token; a busier target keeps its evicted backup rather than read more.
const nfoCommitCleanupIdentityLimit = 1024

// ListNFOWriteCommitCleanups lists tokens of resolved jobs that retained files
// (a plan was recorded), were not cleaned and have no live cleanup lease.
func (s *Store) ListNFOWriteCommitCleanups(ctx context.Context, limit int) ([]string, error) {
	if ctx == nil || limit < 1 || limit > 100 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT w.token::text FROM nfo_write_commit_journal w
 JOIN nfo_write_commit_resolutions r ON r.job_id=w.job_id
 JOIN nfo_write_commit_file_plans p ON p.token=w.token
 WHERE NOT EXISTS(SELECT 1 FROM nfo_write_commit_cleanups c WHERE c.token=w.token)
 AND NOT EXISTS(SELECT 1 FROM nfo_write_commit_cleanup_leases l WHERE l.token=w.token AND l.lease_until>clock_timestamp())
 ORDER BY r.recorded_at,w.token LIMIT $1`, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return nil, storageError(err)
		}
		result = append(result, token)
	}
	return result, storageError(rows.Err())
}

// AcquireNFOWriteCommitCleanup takes the cleanup lease of a resolved token. A
// live lease held by anyone is never replaced (ErrConflict); an expired one
// passes to the new owner with the next epoch. SQL refuses an unresolved job
// and a token whose cleanup is already recorded.
func (s *Store) AcquireNFOWriteCommitCleanup(ctx context.Context, token, owner string, ttl time.Duration) (domain.NFOWriteCommitCleanupLease, error) {
	if ctx == nil || !domain.ValidID(token) || !validText(owner, 128, false) || !validLeaseDuration(ttl) {
		return domain.NFOWriteCommitCleanupLease{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.NFOWriteCommitCleanupLease{}, err
	}
	defer tx.Rollback(ctx)
	lease := domain.NFOWriteCommitCleanupLease{Token: token, Owner: owner}
	err = tx.QueryRow(ctx, `INSERT INTO nfo_write_commit_cleanup_leases(token,owner,epoch,recorded_at,lease_until) VALUES($1::uuid,$2,1,clock_timestamp(),clock_timestamp()+$3*interval '1 millisecond')
 ON CONFLICT(token) DO UPDATE SET owner=EXCLUDED.owner,epoch=nfo_write_commit_cleanup_leases.epoch+1,lease_until=EXCLUDED.lease_until
 WHERE nfo_write_commit_cleanup_leases.lease_until<=clock_timestamp() RETURNING epoch,lease_until`, token, owner, ttl.Milliseconds()).Scan(&lease.Epoch, &lease.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NFOWriteCommitCleanupLease{}, domain.ErrConflict
	}
	if err != nil {
		return domain.NFOWriteCommitCleanupLease{}, storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOWriteCommitCleanupLease{}, storageError(err)
	}
	return lease, nil
}

// RenewNFOWriteCommitCleanup extends a cleanup lease that is still held.
func (s *Store) RenewNFOWriteCommitCleanup(ctx context.Context, lease domain.NFOWriteCommitCleanupLease, ttl time.Duration) (domain.NFOWriteCommitCleanupLease, error) {
	if ctx == nil || !validCleanupLease(lease) || !validLeaseDuration(ttl) {
		return domain.NFOWriteCommitCleanupLease{}, domain.ErrInvalid
	}
	renewed := lease
	err := s.Pool.QueryRow(ctx, `UPDATE nfo_write_commit_cleanup_leases SET lease_until=clock_timestamp()+$4*interval '1 millisecond'
 WHERE token=$1::uuid AND owner=$2 AND epoch=$3 AND lease_until>clock_timestamp() RETURNING lease_until`, lease.Token, lease.Owner, lease.Epoch, ttl.Milliseconds()).Scan(&renewed.ExpiresAt)
	if err != nil {
		// A recorded cleanup also refuses renewal; either way the hold is over.
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(storageError(err), domain.ErrInvalid) {
			return domain.NFOWriteCommitCleanupLease{}, domain.ErrJobLeaseLost
		}
		return domain.NFOWriteCommitCleanupLease{}, storageError(err)
	}
	return renewed, nil
}

func validCleanupLease(l domain.NFOWriteCommitCleanupLease) bool {
	return domain.ValidID(l.Token) && validText(l.Owner, 128, false) && l.Epoch > 0
}

// cleanupLeaseHeld checks the lease inside a transaction and locks its row,
// so a takeover cannot interleave with the read or the record.
func cleanupLeaseHeld(ctx context.Context, tx pgx.Tx, l domain.NFOWriteCommitCleanupLease) error {
	var held bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nfo_write_commit_cleanup_leases WHERE token=$1::uuid AND owner=$2 AND epoch=$3 AND lease_until>clock_timestamp() FOR UPDATE)`, l.Token, l.Owner, l.Epoch).Scan(&held)
	if err != nil {
		return storageError(err)
	}
	if !held {
		return domain.ErrJobLeaseLost
	}
	return nil
}

// GetNFOWriteCommitCleanupInput reads the durable identities a cleaner checks
// every object against: the plan, the identities recorded for each allocated
// attempt namespace, and the originals of earlier settled plans for the same
// parent and name (the only objects a backup rotation can evict).
func (s *Store) GetNFOWriteCommitCleanupInput(ctx context.Context, lease domain.NFOWriteCommitCleanupLease) (domain.NFOWriteCommitCleanupInput, error) {
	if ctx == nil || !validCleanupLease(lease) {
		return domain.NFOWriteCommitCleanupInput{}, domain.ErrInvalid
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return domain.NFOWriteCommitCleanupInput{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	if err := cleanupLeaseHeld(ctx, tx, lease); err != nil {
		return domain.NFOWriteCommitCleanupInput{}, err
	}
	input := domain.NFOWriteCommitCleanupInput{Lease: lease}
	var version int16
	var parent, target []byte
	err = tx.QueryRow(ctx, `SELECT e.root_path,e.relative_path,p.version,p.target_name,p.parent_identity,p.target_identity
 FROM nfo_write_commit_journal w JOIN nfo_write_entries e ON e.job_id=w.job_id AND e.sequence=w.sequence
 JOIN nfo_write_commit_file_plans p ON p.token=w.token WHERE w.token=$1::uuid`, lease.Token).Scan(&input.RootPath, &input.RelativePath, &version, &input.Plan.TargetName, &parent, &target)
	if err != nil {
		return domain.NFOWriteCommitCleanupInput{}, storageError(err)
	}
	if version != 1 || len(parent) != 48 || len(target) != 48 {
		return domain.NFOWriteCommitCleanupInput{}, domain.ErrDatabase
	}
	input.Plan.Version = 1
	copy(input.Plan.ParentIdentity[:], parent)
	copy(input.Plan.TargetIdentity[:], target)
	// Attempt 0 is the legacy namespace; older plans recorded its ready pair
	// without checkpoints, so fall back to the ready row.
	rows, err := tx.Query(ctx, `SELECT n.attempt,
 CASE WHEN n.attempt=0 THEN COALESCE((SELECT output_identity FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid AND phase=1),(SELECT output_identity FROM nfo_write_commit_files_ready WHERE token=$1::uuid))
  ELSE (SELECT output_identity FROM nfo_write_commit_attempt_checkpoints WHERE token=$1::uuid AND attempt=n.attempt AND phase=1) END,
 CASE WHEN n.attempt=0 THEN COALESCE((SELECT rollback_identity FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid AND phase=2),(SELECT rollback_identity FROM nfo_write_commit_files_ready WHERE token=$1::uuid))
  ELSE (SELECT rollback_identity FROM nfo_write_commit_attempt_checkpoints WHERE token=$1::uuid AND attempt=n.attempt AND phase=2) END
 FROM (SELECT 0::smallint AS attempt UNION SELECT attempt FROM nfo_write_commit_attempts WHERE token=$1::uuid) n ORDER BY n.attempt`, lease.Token)
	if err != nil {
		return domain.NFOWriteCommitCleanupInput{}, storageError(err)
	}
	for rows.Next() {
		var attempt int16
		var output, rollback []byte
		if err := rows.Scan(&attempt, &output, &rollback); err != nil {
			rows.Close()
			return domain.NFOWriteCommitCleanupInput{}, storageError(err)
		}
		if attempt < 0 || uint8(attempt) > domain.NFOWriteCommitAttemptLimit || output != nil && len(output) != 48 || rollback != nil && len(rollback) != 48 {
			rows.Close()
			return domain.NFOWriteCommitCleanupInput{}, domain.ErrDatabase
		}
		namespace := domain.NFOWriteCommitCleanupNamespace{Attempt: uint8(attempt)}
		copy(namespace.OutputIdentity[:], output)
		copy(namespace.RollbackIdentity[:], rollback)
		input.Namespaces = append(input.Namespaces, namespace)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.NFOWriteCommitCleanupInput{}, storageError(err)
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT p.target_identity FROM nfo_write_commit_file_plans p
 WHERE p.token<>$1::uuid AND p.parent_identity=$2 AND p.target_name=$3
 AND EXISTS(SELECT 1 FROM nfo_write_commit_settlements s WHERE s.token=p.token AND s.phase=1) LIMIT $4`, lease.Token, parent, input.Plan.TargetName, nfoCommitCleanupIdentityLimit)
	if err != nil {
		return domain.NFOWriteCommitCleanupInput{}, storageError(err)
	}
	for rows.Next() {
		var identity []byte
		if err := rows.Scan(&identity); err != nil {
			rows.Close()
			return domain.NFOWriteCommitCleanupInput{}, storageError(err)
		}
		if len(identity) != 48 {
			rows.Close()
			return domain.NFOWriteCommitCleanupInput{}, domain.ErrDatabase
		}
		input.EvictedIdentities = append(input.EvictedIdentities, [48]byte(identity))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.NFOWriteCommitCleanupInput{}, storageError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.NFOWriteCommitCleanupInput{}, storageError(err)
	}
	return input, nil
}

// CompleteNFOWriteCommitCleanup records the outcome under the held lease and
// so releases the token's attempt reservation. Replay by the same holder with
// the same outcome succeeds; any other recorded outcome is ErrConflict.
func (s *Store) CompleteNFOWriteCommitCleanup(ctx context.Context, lease domain.NFOWriteCommitCleanupLease, outcome domain.NFOWriteCommitCleanupOutcome, retained int) error {
	if ctx == nil || !validCleanupLease(lease) || !outcome.Valid() || retained < 0 || retained > domain.NFOWriteCommitCleanupMaxRetained || (outcome == domain.NFOWriteCommitCleanupRetained) != (retained > 0) {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var owner string
	var epoch int64
	var recordedOutcome, recordedRetained int16
	err = tx.QueryRow(ctx, `SELECT owner,epoch,outcome,retained FROM nfo_write_commit_cleanups WHERE token=$1::uuid`, lease.Token).Scan(&owner, &epoch, &recordedOutcome, &recordedRetained)
	switch {
	case err == nil:
		if owner != lease.Owner || epoch != lease.Epoch || recordedOutcome != int16(outcome) || int(recordedRetained) != retained {
			return domain.ErrConflict
		}
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return storageError(err)
	}
	if err := cleanupLeaseHeld(ctx, tx, lease); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO nfo_write_commit_cleanups(token,owner,epoch,outcome,retained,recorded_at) VALUES($1::uuid,$2,$3,$4,$5,clock_timestamp())`, lease.Token, lease.Owner, lease.Epoch, int16(outcome), int16(retained)); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}
