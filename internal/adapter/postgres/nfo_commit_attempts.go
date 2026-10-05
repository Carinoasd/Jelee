package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.NFOWriteCommitAttemptRepository = (*Store)(nil)

func lockNFOCommitAttemptQuota(ctx context.Context, tx pgx.Tx, optional bool) error {
	if optional {
		var present bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass('nfo_commit_attempt_quota_fence') IS NOT NULL`).Scan(&present); err != nil {
			return storageError(err)
		}
		if !present {
			return nil
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE nfo_commit_attempt_quota_fence SET slot=slot WHERE slot=1`)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrDatabase
	}
	return nil
}

func (s *Store) nfoCommitAttemptTransaction(ctx context.Context, lease domain.JobLease, sequence int, token string, quota bool) (pgx.Tx, error) {
	if ctx == nil || !domain.ValidID(token) || sequence < 1 || sequence > 100 {
		return nil, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nil, err
	}
	if quota {
		err = lockNFOCommitAttemptQuota(ctx, tx, false)
	}
	if err == nil {
		err = checkNFOCommitFileToken(ctx, tx, lease, sequence, token)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func finishNFOCommitAttempt(ctx context.Context, tx pgx.Tx, lease domain.JobLease, sequence int) error {
	if err := checkNFOCommitCatalogScope(ctx, tx, lease.Job.ID, sequence); err != nil {
		return err
	}
	if _, err := fencedNFOCommitLease(ctx, tx, lease); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
func scanNFOCommitAttemptReservation(row pgx.Row) (domain.NFOWriteCommitAttemptReservation, error) {
	var v domain.NFOWriteCommitAttemptReservation
	var original, replacement []byte
	if err := row.Scan(&v.OriginalBytes, &v.ReplacementBytes, &original, &replacement, &v.RetainedBytes); err != nil {
		return v, err
	}
	if len(original) != 32 || len(replacement) != 32 {
		return domain.NFOWriteCommitAttemptReservation{}, domain.ErrDatabase
	}
	copy(v.OriginalHash[:], original)
	copy(v.ReplacementHash[:], replacement)
	if domain.ValidateNFOWriteCommitAttemptReservation(v) != nil {
		return domain.NFOWriteCommitAttemptReservation{}, domain.ErrDatabase
	}
	return v, nil
}

const nfoAttemptReservationColumns = `original_bytes,replacement_bytes,original_hash,replacement_hash,retained_bytes`

func (s *Store) ReserveNFOWriteCommitAttempts(ctx context.Context, lease domain.JobLease, sequence int, token string, value domain.NFOWriteCommitAttemptReservation) (domain.NFOWriteCommitAttemptReservation, error) {
	zero := domain.NFOWriteCommitAttemptReservation{}
	if domain.ValidateNFOWriteCommitAttemptReservation(value) != nil {
		return zero, domain.ErrInvalid
	}
	tx, err := s.nfoCommitAttemptTransaction(ctx, lease, sequence, token, true)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO nfo_write_commit_attempt_reservations(token,original_bytes,replacement_bytes,original_hash,replacement_hash,retained_bytes) VALUES($1::uuid,$2,$3,$4,$5,$6) ON CONFLICT(token) DO NOTHING`, token, value.OriginalBytes, value.ReplacementBytes, value.OriginalHash[:], value.ReplacementHash[:], value.RetainedBytes); err != nil {
		return zero, storageError(err)
	}
	saved, err := scanNFOCommitAttemptReservation(tx.QueryRow(ctx, `UPDATE nfo_write_commit_attempt_reservations SET token=token WHERE token=$1::uuid RETURNING `+nfoAttemptReservationColumns, token))
	if err != nil {
		return zero, storageError(err)
	}
	if saved != value {
		return zero, domain.ErrConflict
	}
	if err = finishNFOCommitAttempt(ctx, tx, lease, sequence); err != nil {
		return zero, err
	}
	return saved, nil
}

func (s *Store) AllocateNFOWriteCommitAttempt(ctx context.Context, lease domain.JobLease, sequence int, token string, after uint8) (uint8, error) {
	if after >= domain.NFOWriteCommitAttemptLimit {
		return 0, domain.ErrInvalid
	}
	tx, err := s.nfoCommitAttemptTransaction(ctx, lease, sequence, token, false)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var latest int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(attempt),-1) FROM nfo_write_commit_attempts WHERE token=$1::uuid`, token).Scan(&latest); err != nil {
		return 0, storageError(err)
	}
	if latest < int(after) {
		return 0, domain.ErrConflict
	}
	next := after + 1
	if _, err = tx.Exec(ctx, `INSERT INTO nfo_write_commit_attempts(token,attempt,previous_attempt) VALUES($1::uuid,$2,$3) ON CONFLICT(token,attempt) DO NOTHING`, token, next, after); err != nil {
		return 0, storageError(err)
	}
	var saved uint8
	if err = tx.QueryRow(ctx, `UPDATE nfo_write_commit_attempts SET token=token WHERE token=$1::uuid AND attempt=$2 RETURNING attempt`, token, next).Scan(&saved); err != nil {
		return 0, storageError(err)
	}
	if saved != next {
		return 0, domain.ErrDatabase
	}
	if err = finishNFOCommitAttempt(ctx, tx, lease, sequence); err != nil {
		return 0, err
	}
	return saved, nil
}

func (s *Store) SaveNFOWriteCommitAttemptCheckpoint(ctx context.Context, lease domain.JobLease, sequence int, token string, attempt uint8, value domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	zero := domain.NFOWriteCommitFileCheckpoint{}
	if attempt < 1 || attempt > domain.NFOWriteCommitAttemptLimit || domain.ValidateNFOWriteCommitFileCheckpoint(value) != nil {
		return zero, domain.ErrInvalid
	}
	tx, err := s.nfoCommitAttemptTransaction(ctx, lease, sequence, token, false)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	var rollback any
	if value.Phase == 2 {
		rollback = value.RollbackIdentity[:]
	}
	if _, err = tx.Exec(ctx, `INSERT INTO nfo_write_commit_attempt_checkpoints(token,attempt,phase,output_identity,rollback_identity) VALUES($1::uuid,$2,$3,$4,$5) ON CONFLICT(token,attempt,phase) DO NOTHING`, token, attempt, value.Phase, value.OutputIdentity[:], rollback); err != nil {
		return zero, storageError(err)
	}
	var saved domain.NFOWriteCommitFileCheckpoint
	var output, back []byte
	if err = tx.QueryRow(ctx, `UPDATE nfo_write_commit_attempt_checkpoints SET token=token WHERE token=$1::uuid AND attempt=$2 AND phase=$3 RETURNING phase,output_identity,rollback_identity`, token, attempt, value.Phase).Scan(&saved.Phase, &output, &back); err != nil {
		return zero, storageError(err)
	}
	if len(output) != 48 || (saved.Phase == 1 && back != nil) || (saved.Phase == 2 && len(back) != 48) {
		return zero, domain.ErrDatabase
	}
	copy(saved.OutputIdentity[:], output)
	copy(saved.RollbackIdentity[:], back)
	if saved != value {
		return zero, domain.ErrConflict
	}
	if err = finishNFOCommitAttempt(ctx, tx, lease, sequence); err != nil {
		return zero, err
	}
	return saved, nil
}

func (s *Store) SaveNFOWriteCommitAttemptReady(ctx context.Context, lease domain.JobLease, sequence int, token string, attempt uint8, value domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	zero := domain.NFOWriteCommitFilesReady{}
	if attempt < 1 || attempt > domain.NFOWriteCommitAttemptLimit || domain.ValidateNFOWriteCommitFilesReady(value) != nil {
		return zero, domain.ErrInvalid
	}
	tx, err := s.nfoCommitAttemptTransaction(ctx, lease, sequence, token, false)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO nfo_write_commit_attempt_ready(token,attempt,output_identity,rollback_identity) VALUES($1::uuid,$2,$3,$4) ON CONFLICT(token) DO NOTHING`, token, attempt, value.OutputIdentity[:], value.RollbackIdentity[:]); err != nil {
		return zero, storageError(err)
	}
	var saved domain.NFOWriteCommitFilesReady
	var number uint8
	var output, back []byte
	if err = tx.QueryRow(ctx, `UPDATE nfo_write_commit_attempt_ready SET token=token WHERE token=$1::uuid RETURNING attempt,output_identity,rollback_identity`, token).Scan(&number, &output, &back); err != nil {
		return zero, storageError(err)
	}
	if len(output) != 48 || len(back) != 48 {
		return zero, domain.ErrDatabase
	}
	copy(saved.OutputIdentity[:], output)
	copy(saved.RollbackIdentity[:], back)
	if number != attempt || saved != value {
		return zero, domain.ErrConflict
	}
	if err = finishNFOCommitAttempt(ctx, tx, lease, sequence); err != nil {
		return zero, err
	}
	return saved, nil
}

func (s *Store) GetNFOWriteCommitAttempts(ctx context.Context, lease domain.JobLease, sequence int, token string) (domain.NFOWriteCommitAttempts, error) {
	zero := domain.NFOWriteCommitAttempts{}
	tx, err := s.nfoCommitAttemptTransaction(ctx, lease, sequence, token, false)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	value := zero
	value.Reservation, err = scanNFOCommitAttemptReservation(tx.QueryRow(ctx, `SELECT `+nfoAttemptReservationColumns+` FROM nfo_write_commit_attempt_reservations WHERE token=$1::uuid`, token))
	if errors.Is(err, pgx.ErrNoRows) {
		if err = finishNFOCommitAttempt(ctx, tx, lease, sequence); err != nil {
			return zero, err
		}
		return zero, nil
	}
	if err != nil {
		return zero, storageError(err)
	}
	value.ReservationRecorded = true
	rows, err := tx.Query(ctx, `SELECT a.attempt,COALESCE(c.phase,0),c.output_identity,c.rollback_identity,r.output_identity,r.rollback_identity FROM nfo_write_commit_attempts a LEFT JOIN LATERAL(SELECT phase,output_identity,rollback_identity FROM nfo_write_commit_attempt_checkpoints WHERE token=a.token AND attempt=a.attempt ORDER BY phase DESC LIMIT 1)c ON true LEFT JOIN nfo_write_commit_attempt_ready r ON r.token=a.token AND r.attempt=a.attempt WHERE a.token=$1::uuid AND a.attempt>0 ORDER BY a.attempt LIMIT 3`, token)
	if err != nil {
		return zero, storageError(err)
	}
	index := 0
	for rows.Next() {
		var entry domain.NFOWriteCommitAttempt
		var output, back, readyOutput, readyBack []byte
		if err = rows.Scan(&entry.Number, &entry.Checkpoint.Phase, &output, &back, &readyOutput, &readyBack); err != nil {
			rows.Close()
			return zero, storageError(err)
		}
		if index >= 3 || int(entry.Number) != index+1 {
			rows.Close()
			return zero, domain.ErrDatabase
		}
		if entry.Checkpoint.Phase > 0 {
			if len(output) != 48 || (entry.Checkpoint.Phase == 1 && back != nil) || (entry.Checkpoint.Phase == 2 && len(back) != 48) {
				rows.Close()
				return zero, domain.ErrDatabase
			}
			copy(entry.Checkpoint.OutputIdentity[:], output)
			copy(entry.Checkpoint.RollbackIdentity[:], back)
			if domain.ValidateNFOWriteCommitFileCheckpoint(entry.Checkpoint) != nil {
				rows.Close()
				return zero, domain.ErrDatabase
			}
			entry.CheckpointRecorded = true
		} else if output != nil || back != nil {
			rows.Close()
			return zero, domain.ErrDatabase
		}
		if readyOutput != nil {
			if len(readyOutput) != 48 || len(readyBack) != 48 || !entry.CheckpointRecorded || entry.Checkpoint.Phase != 2 {
				rows.Close()
				return zero, domain.ErrDatabase
			}
			copy(entry.Ready.OutputIdentity[:], readyOutput)
			copy(entry.Ready.RollbackIdentity[:], readyBack)
			if entry.Ready.OutputIdentity != entry.Checkpoint.OutputIdentity || entry.Ready.RollbackIdentity != entry.Checkpoint.RollbackIdentity {
				rows.Close()
				return zero, domain.ErrDatabase
			}
			entry.ReadyRecorded = true
		} else if readyBack != nil {
			rows.Close()
			return zero, domain.ErrDatabase
		}
		value.Attempts[index] = entry
		index++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, storageError(err)
	}
	if err = finishNFOCommitAttempt(ctx, tx, lease, sequence); err != nil {
		return zero, err
	}
	return value, nil
}
