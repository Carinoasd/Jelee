package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.NFOWriteCommitFilesRepository = (*Store)(nil)

func (s *Store) BeginNFOWriteCommit(ctx context.Context, lease domain.JobLease, sequence int) (domain.NFOWriteCommitRecord, error) {
	if ctx == nil {
		return domain.NFOWriteCommitRecord{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.NFOWriteCommitRecord{}, err
	}
	defer tx.Rollback(ctx)
	record, err := recordNFOWriteCommit(ctx, tx, lease, sequence)
	if err != nil {
		return domain.NFOWriteCommitRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.NFOWriteCommitRecord{}, storageError(err)
	}
	return record, nil
}

func (s *Store) SaveNFOWriteCommitFilePlan(ctx context.Context, lease domain.JobLease, sequence int, token string, plan domain.NFOWriteCommitFilePlan) (domain.NFOWriteCommitFilePlan, error) {
	if ctx == nil || !domain.ValidID(token) || domain.ValidateNFOWriteCommitFilePlan(plan) != nil {
		return domain.NFOWriteCommitFilePlan{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.NFOWriteCommitFilePlan{}, err
	}
	defer tx.Rollback(ctx)
	if err := checkNFOCommitFileToken(ctx, tx, lease, sequence, token); err != nil {
		return domain.NFOWriteCommitFilePlan{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO nfo_write_commit_file_plans(token,version,target_name,parent_identity,target_identity) VALUES($1::uuid,$2,$3,$4,$5) ON CONFLICT(token) DO NOTHING`, token, plan.Version, plan.TargetName, plan.ParentIdentity[:], plan.TargetIdentity[:]); err != nil {
		return domain.NFOWriteCommitFilePlan{}, storageError(err)
	}
	var saved domain.NFOWriteCommitFilePlan
	var parent, target []byte
	// No-op UPDATE fires the deferred live-lease check on replay too.
	err = tx.QueryRow(ctx, `UPDATE nfo_write_commit_file_plans SET token=token WHERE token=$1::uuid RETURNING version,target_name,parent_identity,target_identity`, token).Scan(&saved.Version, &saved.TargetName, &parent, &target)
	if err != nil {
		return domain.NFOWriteCommitFilePlan{}, storageError(err)
	}
	if len(parent) != 48 || len(target) != 48 {
		return domain.NFOWriteCommitFilePlan{}, domain.ErrInvalid
	}
	copy(saved.ParentIdentity[:], parent)
	copy(saved.TargetIdentity[:], target)
	if saved != plan {
		return domain.NFOWriteCommitFilePlan{}, domain.ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.NFOWriteCommitFilePlan{}, storageError(err)
	}
	return saved, nil
}

func (s *Store) SaveNFOWriteCommitFilesReady(ctx context.Context, lease domain.JobLease, sequence int, token string, ready domain.NFOWriteCommitFilesReady) (domain.NFOWriteCommitFilesReady, error) {
	if ctx == nil || !domain.ValidID(token) || domain.ValidateNFOWriteCommitFilesReady(ready) != nil {
		return domain.NFOWriteCommitFilesReady{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.NFOWriteCommitFilesReady{}, err
	}
	defer tx.Rollback(ctx)
	if err := checkNFOCommitFileToken(ctx, tx, lease, sequence, token); err != nil {
		return domain.NFOWriteCommitFilesReady{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO nfo_write_commit_files_ready(token,output_identity,rollback_identity) VALUES($1::uuid,$2,$3) ON CONFLICT(token) DO NOTHING`, token, ready.OutputIdentity[:], ready.RollbackIdentity[:]); err != nil {
		return domain.NFOWriteCommitFilesReady{}, storageError(err)
	}
	var saved domain.NFOWriteCommitFilesReady
	var output, rollback []byte
	if err := tx.QueryRow(ctx, `UPDATE nfo_write_commit_files_ready SET token=token WHERE token=$1::uuid RETURNING output_identity,rollback_identity`, token).Scan(&output, &rollback); err != nil {
		return domain.NFOWriteCommitFilesReady{}, storageError(err)
	}
	if len(output) != 48 || len(rollback) != 48 {
		return domain.NFOWriteCommitFilesReady{}, domain.ErrInvalid
	}
	copy(saved.OutputIdentity[:], output)
	copy(saved.RollbackIdentity[:], rollback)
	if saved != ready {
		return domain.NFOWriteCommitFilesReady{}, domain.ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.NFOWriteCommitFilesReady{}, storageError(err)
	}
	return saved, nil
}

func checkNFOCommitFileToken(ctx context.Context, tx pgx.Tx, lease domain.JobLease, sequence int, token string) error {
	record, err := recordNFOWriteCommit(ctx, tx, lease, sequence)
	if err != nil {
		return err
	}
	if record.Token != token {
		return domain.ErrConflict
	}
	var existing string
	if err := tx.QueryRow(ctx, `SELECT token::text FROM nfo_write_commit_journal WHERE token=$1::uuid AND job_id=$2::uuid AND sequence=$3 AND generation=$4 AND owner=$5`, token, lease.Job.ID, sequence, lease.Generation, lease.Owner).Scan(&existing); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrJobLeaseLost
		}
		return storageError(err)
	}
	return nil
}
