package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.NFOWriteTaskRepository = (*Store)(nil)

// The admission transaction owns authorization, current scope and job creation.
// This private step copies immutable data; it cannot create an executable job.
func copyNFOWriteIntents(ctx context.Context, tx pgx.Tx, job string, ids []string) error {
	if len(ids) < 1 || len(ids) > 100 {
		return domain.ErrInvalid
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !domain.ValidID(id) || seen[id] {
			return domain.ErrInvalid
		}
		seen[id] = true
	}
	var actor, library, kind, priority string
	if err := tx.QueryRow(ctx, `SELECT actor_id::text,library_id::text,kind,priority FROM jobs WHERE id=$1::uuid FOR UPDATE`, job).Scan(&actor, &library, &kind, &priority); err != nil {
		return storageError(err)
	}
	if kind != domain.JobNFOWrite {
		return domain.ErrInvalid
	}
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT generation FROM nfo_write_preparations WHERE id=$1::uuid AND actor_id=$2::uuid AND library_id=$3::uuid AND expires_at>clock_timestamp()`, ids[0], actor, library).Scan(&generation); err != nil {
		return storageError(err)
	}
	encoded, _ := json.Marshal(struct {
		Priority     string
		Preparations []string
	}{priority, ids})
	digest := sha256.Sum256(encoded)
	if _, err := tx.Exec(ctx, `INSERT INTO nfo_write_requests(job_id,library_id,generation,total,intent_digest) VALUES($1::uuid,$2::uuid,$3,$4,$5)`, job, library, generation, len(ids), digest[:]); err != nil {
		return storageError(err)
	}
	for i, id := range ids {
		tag, err := tx.Exec(ctx, `INSERT INTO nfo_write_entries(job_id,sequence,preparation_id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256)
 SELECT $1::uuid,$2,id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256 FROM nfo_write_preparations WHERE id=$3::uuid AND actor_id=$4::uuid AND library_id=$5::uuid AND generation=$6 AND expires_at>clock_timestamp()`, job, i+1, id, actor, library, generation)
		if err != nil {
			return storageError(err)
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrConflict
		}
	}
	return nil
}

// Reading owned task data is not filesystem execution authorization. The write
// worker must implement fresh policy/media checks and durable commit recovery.
func (s *Store) GetNFOWriteTask(ctx context.Context, lease domain.JobLease, sequence int) (domain.NFOWriteTask, error) {
	if ctx == nil || sequence < 1 || sequence > 100 {
		return domain.NFOWriteTask{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.NFOWriteTask{}, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, lease)
	if err != nil {
		return domain.NFOWriteTask{}, err
	}
	if current.Job.Kind != domain.JobNFOWrite {
		return domain.NFOWriteTask{}, domain.ErrInvalid
	}
	if current.Job.CancelRequested {
		return domain.NFOWriteTask{}, context.Canceled
	}
	var actor string
	if err := tx.QueryRow(ctx, `SELECT u.id::text FROM jobs j JOIN users u ON u.id=j.actor_id WHERE j.id=$1::uuid AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF u`, lease.Job.ID).Scan(&actor); err != nil {
		if err == pgx.ErrNoRows {
			return domain.NFOWriteTask{}, domain.ErrForbidden
		}
		return domain.NFOWriteTask{}, storageError(err)
	}
	result := domain.NFOWriteTask{JobID: current.Job.ID, Sequence: sequence}
	p := &result.Preparation
	var requestBytes []byte
	var digest string
	var maxBytes int64
	err = tx.QueryRow(ctx, `SELECT preparation_id::text,version,request_bytes,request_digest,library_id::text,item_id::text,source_id::text,root_id::text,kind,revision,generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes FROM nfo_write_entries WHERE job_id=$1::uuid AND sequence=$2`, lease.Job.ID, sequence).Scan(&p.ID, &p.Version, &requestBytes, &digest, &p.Scope.LibraryID, &p.Scope.ItemID, &p.Scope.SourceID, &p.Scope.RootID, &p.Scope.Kind, &p.Scope.Revision, &p.Scope.Generation, &p.Scope.Source.RootPath, &p.Scope.Source.RelativePath, &p.Scope.MediaPath, &p.Scope.DirectoryPath, &maxBytes, &p.Stamp.ModifiedUnixNano, &p.Original, &p.Stamp.SHA256, &p.Replacement)
	if err != nil {
		return domain.NFOWriteTask{}, storageError(err)
	}
	p.Stamp.Size = int64(len(p.Original))
	p.Stamp.FingerprintVersion = domain.NFOFingerprintVersion
	if json.Unmarshal(requestBytes, &p.Request) != nil || p.Request.MaxBytes != maxBytes || domain.ValidateNFOWritePreparation(*p) != nil {
		return domain.NFOWriteTask{}, domain.ErrDatabase
	}
	actual, _ := domain.NFOWriteRequestDigest(p.Request)
	if actual != digest {
		return domain.NFOWriteTask{}, domain.ErrDatabase
	}
	var live bool
	if err = tx.QueryRow(ctx, `SELECT state='running' AND owner=$2 AND generation=$3 AND lease_until>clock_timestamp() AND NOT cancel_requested FROM jobs WHERE id=$1::uuid`, lease.Job.ID, lease.Owner, lease.Generation).Scan(&live); err != nil {
		return domain.NFOWriteTask{}, storageError(err)
	}
	if !live {
		return domain.NFOWriteTask{}, domain.ErrJobLeaseLost
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOWriteTask{}, storageError(err)
	}
	return result, nil
}
