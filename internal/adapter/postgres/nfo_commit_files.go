package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.NFOWriteCommitFilesRepository = (*Store)(nil)

// GetNFOWriteCommitFiles observes an existing journal; it never creates one or
// resolves retained evidence. A subsequent execution needs fresh commit fences.
func (s *Store) GetNFOWriteCommitFiles(ctx context.Context, lease domain.JobLease, sequence int, token string) (domain.NFOWriteCommitFileEvidence, error) {
	zero := domain.NFOWriteCommitFileEvidence{}
	if ctx == nil || sequence < 1 || sequence > 100 || !domain.ValidID(token) {
		return zero, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, lease)
	if err != nil {
		return zero, err
	}
	if current.Job.Kind != domain.JobNFOWrite {
		return zero, domain.ErrInvalid
	}
	if current.Job.CancelRequested {
		return zero, context.Canceled
	}
	var actor string
	if err := tx.QueryRow(ctx, `SELECT u.id::text FROM jobs j JOIN users u ON u.id=j.actor_id WHERE j.id=$1::uuid AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF u`, lease.Job.ID).Scan(&actor); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, domain.ErrForbidden
		}
		return zero, storageError(err)
	}
	var value domain.NFOWriteCommitFileEvidence
	var parent, target, output, rollback []byte
	err = tx.QueryRow(ctx, `SELECT w.job_id::text,w.sequence,w.generation,w.token::text,w.owner,w.recorded_at,w.lease_until,p.token IS NOT NULL,r.token IS NOT NULL,COALESCE(p.version,0),COALESCE(p.target_name,''),p.parent_identity,p.target_identity,r.output_identity,r.rollback_identity FROM nfo_write_commit_journal w LEFT JOIN nfo_write_commit_file_plans p ON p.token=w.token LEFT JOIN nfo_write_commit_files_ready r ON r.token=w.token WHERE w.token=$1::uuid AND w.job_id=$2::uuid AND w.sequence=$3 AND w.generation=$4 AND w.owner=$5`, token, lease.Job.ID, sequence, lease.Generation, lease.Owner).Scan(&value.Record.JobID, &value.Record.Sequence, &value.Record.Generation, &value.Record.Token, &value.Record.Owner, &value.Record.RecordedAt, &value.Record.LeaseUntil, &value.PlanRecorded, &value.ReadyRecorded, &value.Plan.Version, &value.Plan.TargetName, &parent, &target, &output, &rollback)
	if err != nil {
		return zero, storageError(err)
	}
	if value.PlanRecorded {
		if len(parent) != 48 || len(target) != 48 {
			return zero, domain.ErrDatabase
		}
		copy(value.Plan.ParentIdentity[:], parent)
		copy(value.Plan.TargetIdentity[:], target)
		if domain.ValidateNFOWriteCommitFilePlan(value.Plan) != nil {
			return zero, domain.ErrDatabase
		}
	}
	if value.ReadyRecorded {
		if !value.PlanRecorded || len(output) != 48 || len(rollback) != 48 {
			return zero, domain.ErrDatabase
		}
		copy(value.Ready.OutputIdentity[:], output)
		copy(value.Ready.RollbackIdentity[:], rollback)
		if domain.ValidateNFOWriteCommitFilesReady(value.Ready) != nil || value.Plan.TargetIdentity[1] != value.Ready.OutputIdentity[1] || value.Plan.TargetIdentity == value.Ready.OutputIdentity || value.Plan.TargetIdentity == value.Ready.RollbackIdentity {
			return zero, domain.ErrDatabase
		}
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT state='running' AND owner=$2 AND generation=$3 AND lease_until>clock_timestamp() AND NOT cancel_requested FROM jobs WHERE id=$1::uuid`, lease.Job.ID, lease.Owner, lease.Generation).Scan(&live); err != nil {
		return zero, storageError(err)
	}
	if !live {
		return zero, domain.ErrJobLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, storageError(err)
	}
	return value, nil
}

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
	if err := checkNFOCommitCatalogScope(ctx, tx, lease.Job.ID, sequence); err != nil {
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
	if err := checkNFOCommitCatalogScope(ctx, tx, lease.Job.ID, sequence); err != nil {
		return domain.NFOWriteCommitFilePlan{}, err
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
	if err := checkNFOCommitCatalogScope(ctx, tx, lease.Job.ID, sequence); err != nil {
		return domain.NFOWriteCommitFilesReady{}, err
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
	return checkNFOCommitCatalogScope(ctx, tx, lease.Job.ID, sequence)
}

// Lock and compare the current repository scope with the immutable job intent.
// This fences application plan/ready transactions, not direct SQL or native
// root/media identity; it does not grant filesystem commit authorization.
func checkNFOCommitCatalogScope(ctx context.Context, tx pgx.Tx, job string, sequence int) error {
	var saved domain.NFOItemScope
	err := tx.QueryRow(ctx, `SELECT e.item_id::text,e.library_id::text,e.source_id::text,e.root_id::text,e.kind,e.revision,e.generation,e.directory_path,e.media_path,e.root_path,e.relative_path FROM nfo_write_entries e JOIN jobs j ON j.id=e.job_id AND j.library_id=e.library_id WHERE e.job_id=$1::uuid AND e.sequence=$2`, job, sequence).Scan(&saved.ItemID, &saved.LibraryID, &saved.SourceID, &saved.RootID, &saved.Kind, &saved.Revision, &saved.Generation, &saved.DirectoryPath, &saved.MediaPath, &saved.Source.RootPath, &saved.Source.RelativePath)
	if err != nil {
		return storageError(err)
	}
	if !domain.ValidNFOItemScope(saved) {
		return domain.ErrDatabase
	}
	live, err := readItemNFOScope(ctx, tx, saved.ItemID, saved.Revision)
	if err != nil {
		return err
	}
	if live != saved {
		return domain.ErrConflict
	}
	// Metadata's item row is already locked. Lock its revision row too so even
	// a direct update cannot change the compared revision before this TX ends.
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM item_metadata_state WHERE item_id=$1::uuid FOR UPDATE`, saved.ItemID).Scan(&revision); err != nil {
		// Revision one has no state row. The held item FOR UPDATE lock blocks
		// insertion through the state's item FK until this transaction ends.
		if errors.Is(err, pgx.ErrNoRows) && saved.Revision == 1 {
			return nil
		}
		return storageError(err)
	}
	if revision != saved.Revision {
		return domain.ErrConflict
	}
	return nil
}
