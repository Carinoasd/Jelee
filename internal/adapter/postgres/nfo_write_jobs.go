package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

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
		tag, err := tx.Exec(ctx, `INSERT INTO nfo_write_entries(job_id,sequence,preparation_id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt)
 SELECT $1::uuid,$2,id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt FROM nfo_write_preparations WHERE id=$3::uuid AND actor_id=$4::uuid AND library_id=$5::uuid AND generation=$6 AND root_generation>0 AND native_receipt IS NOT NULL AND expires_at>clock_timestamp()`, job, i+1, id, actor, library, generation)
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
	current, err := fencedNFOCommitLease(ctx, tx, lease)
	if err != nil {
		return domain.NFOWriteTask{}, err
	}
	if current.Job.Kind != domain.JobNFOWrite {
		return domain.NFOWriteTask{}, domain.ErrInvalid
	}
	if current.Job.CancelRequested && lease.RecoveryEpoch == 0 {
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
	var requestBytes, nativeBytes []byte
	var digest string
	var maxBytes int64
	columns, err := nfoHistoricalNativeColumns(ctx, tx, `preparation_id::text,version,request_bytes,request_digest,library_id::text,item_id::text,source_id::text,root_id::text,kind,revision,generation,COALESCE(root_generation,0),root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,COALESCE(native_receipt,NULL::bytea)`, "nfo_write_entries")
	if err != nil {
		return domain.NFOWriteTask{}, err
	}
	err = tx.QueryRow(ctx, `SELECT `+columns+` FROM nfo_write_entries WHERE job_id=$1::uuid AND sequence=$2`, lease.Job.ID, sequence).Scan(&p.ID, &p.Version, &requestBytes, &digest, &p.Scope.LibraryID, &p.Scope.ItemID, &p.Scope.SourceID, &p.Scope.RootID, &p.Scope.Kind, &p.Scope.Revision, &p.Scope.Generation, &p.Scope.RootGeneration, &p.Scope.Source.RootPath, &p.Scope.Source.RelativePath, &p.Scope.MediaPath, &p.Scope.DirectoryPath, &maxBytes, &p.Stamp.ModifiedUnixNano, &p.Original, &p.Stamp.SHA256, &p.Replacement, &nativeBytes)
	if err != nil {
		return domain.NFOWriteTask{}, storageError(err)
	}
	p.NativeObservation, err = readNFONativeReceipt(nativeBytes)
	if err != nil {
		return domain.NFOWriteTask{}, err
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
	if err = nfoCommitLeaseLive(ctx, tx, current); err != nil {
		return domain.NFOWriteTask{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOWriteTask{}, storageError(err)
	}
	return result, nil
}

// SubmitNFOWriteJob admits a batch of the actor's unexpired preparations as one
// queued nfo_write job; the job owns copies of their immutable intents. Replay
// of the same key and intent returns the first job. Execution still rechecks
// the catalog, the native scope and the target before any filesystem change.
func (s *Store) SubmitNFOWriteJob(ctx context.Context, actor domain.Actor, library, key, priority string, preparations []string, policy domain.JobPolicy) (domain.Job, bool, error) {
	if ctx == nil || !domain.ValidID(library) || !validJobKey(key) || !validJobPolicy(policy) || len(preparations) < 1 || len(preparations) > 100 || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	encoded, _ := json.Marshal(struct {
		Priority     string
		Preparations []string
	}{priority, preparations})
	digest := sha256.Sum256(encoded)
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	old, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE actor_id=$1 AND idempotency_key=$2`, actor.UserID, key))
	if err == nil {
		if old.Kind != domain.JobNFOWrite || old.LibraryID != library {
			return domain.Job{}, false, domain.ErrConflict
		}
		var retained []byte
		if err = tx.QueryRow(ctx, `SELECT intent_digest FROM nfo_write_requests WHERE job_id=$1`, old.ID).Scan(&retained); err != nil {
			return domain.Job{}, false, storageError(err)
		}
		if !bytes.Equal(retained, digest[:]) {
			return domain.Job{}, false, domain.ErrConflict
		}
		if err = probeAdminStillLive(ctx, tx, actor); err != nil {
			return domain.Job{}, false, err
		}
		return old, true, storageError(tx.Commit(ctx))
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Job{}, false, err
	}
	var busy bool
	var active int
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE library_id=$1 AND state IN ('queued','running')),(SELECT count(*) FROM jobs WHERE state IN ('queued','running'))`, library).Scan(&busy, &active); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if busy {
		return domain.Job{}, false, domain.ErrJobBusy
	}
	if active >= policy.QueueLimit {
		return domain.Job{}, false, domain.ErrJobQueueFull
	}
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1,$2,$3,'nfo_write',$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+jobColumns, library, actor.UserID, key, priority, policy.QueueLimit, policy.HistoryLimit, policy.MaxEntries, policy.MaxDirectories, policy.MaxAttempts, policy.MissingCountLimit, policy.MissingPercentLimit))
	if err != nil {
		return domain.Job{}, false, err
	}
	if err = copyNFOWriteIntents(ctx, tx, job.ID, preparations); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			err = domain.ErrConflict
		}
		return domain.Job{}, false, err
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "nfo.write_submitted", job.ID, nil, map[string]any{"total": len(preparations)}); err != nil {
		return domain.Job{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.Job{}, false, err
	}
	return job, false, storageError(tx.Commit(ctx))
}
