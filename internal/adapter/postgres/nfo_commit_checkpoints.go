package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.NFOWriteCommitCheckpointRepository = (*Store)(nil)

func (s *Store) SaveNFOWriteCommitFileCheckpoint(ctx context.Context, lease domain.JobLease, sequence int, token string, value domain.NFOWriteCommitFileCheckpoint) (domain.NFOWriteCommitFileCheckpoint, error) {
	zero := domain.NFOWriteCommitFileCheckpoint{}
	if ctx == nil || !domain.ValidID(token) || domain.ValidateNFOWriteCommitFileCheckpoint(value) != nil {
		return zero, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	if err := checkNFOCommitFileToken(ctx, tx, lease, sequence, token); err != nil {
		return zero, err
	}
	var rollback any
	if value.Phase == 2 {
		rollback = value.RollbackIdentity[:]
	}
	if _, err := tx.Exec(ctx, `INSERT INTO nfo_write_commit_file_checkpoints(token,phase,output_identity,rollback_identity) VALUES($1::uuid,$2,$3,$4) ON CONFLICT(token,phase) DO NOTHING`, token, value.Phase, value.OutputIdentity[:], rollback); err != nil {
		return zero, storageError(err)
	}
	var saved domain.NFOWriteCommitFileCheckpoint
	var output, back []byte
	// Replays retain the first timestamp and run immediate/deferred fences.
	if err := tx.QueryRow(ctx, `UPDATE nfo_write_commit_file_checkpoints SET token=token WHERE token=$1::uuid AND phase=$2 RETURNING phase,output_identity,rollback_identity`, token, value.Phase).Scan(&saved.Phase, &output, &back); err != nil {
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
	if err := checkNFOCommitCatalogScope(ctx, tx, lease.Job.ID, sequence); err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, storageError(err)
	}
	return saved, nil
}

func readNFOCommitCheckpoint(ctx context.Context, tx pgx.Tx, token string, evidence *domain.NFOWriteCommitFileEvidence) error {
	var present bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('nfo_write_commit_file_checkpoints') IS NOT NULL`).Scan(&present); err != nil {
		return storageError(err)
	}
	if !present {
		return nil
	} // Owned pre55 fixtures retain their original schema.
	var output, rollback []byte
	err := tx.QueryRow(ctx, `SELECT phase,output_identity,rollback_identity FROM nfo_write_commit_file_checkpoints WHERE token=$1::uuid ORDER BY phase DESC LIMIT 1`, token).Scan(&evidence.Checkpoint.Phase, &output, &rollback)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return storageError(err)
	}
	if !evidence.PlanRecorded || len(output) != 48 || (evidence.Checkpoint.Phase == 1 && rollback != nil) || (evidence.Checkpoint.Phase == 2 && len(rollback) != 48) {
		return domain.ErrDatabase
	}
	copy(evidence.Checkpoint.OutputIdentity[:], output)
	copy(evidence.Checkpoint.RollbackIdentity[:], rollback)
	value := evidence.Checkpoint
	if domain.ValidateNFOWriteCommitFileCheckpoint(value) != nil || value.OutputIdentity[1] != evidence.Plan.TargetIdentity[1] || value.OutputIdentity == evidence.Plan.TargetIdentity || value.RollbackIdentity == evidence.Plan.TargetIdentity {
		return domain.ErrDatabase
	}
	if evidence.ReadyRecorded && (value.Phase != 2 || value.OutputIdentity != evidence.Ready.OutputIdentity || value.RollbackIdentity != evidence.Ready.RollbackIdentity) {
		return domain.ErrDatabase
	}
	evidence.CheckpointRecorded = true
	return nil
}
