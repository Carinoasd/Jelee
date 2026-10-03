package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.NFOWritePreparationRepository = (*Store)(nil)

const nfoWritePreparationColumns = `id::text,version,request_bytes,library_id::text,item_id::text,source_id::text,root_id::text,kind,revision,generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,created_at,expires_at`

func readNFOWritePreparation(ctx context.Context, tx pgx.Tx, actor, key, digest string) (domain.NFOWritePreparation, error) {
	var retained string
	err := tx.QueryRow(ctx, `SELECT request_digest FROM nfo_write_preparations WHERE actor_id=$1::uuid AND idempotency_key=$2 AND expires_at>clock_timestamp()`, actor, key).Scan(&retained)
	if err != nil {
		return domain.NFOWritePreparation{}, storageError(err)
	}
	if retained != digest {
		return domain.NFOWritePreparation{}, domain.ErrConflict
	}
	var result domain.NFOWritePreparation
	var requestBytes []byte
	var maxBytes int64
	err = tx.QueryRow(ctx, `SELECT `+nfoWritePreparationColumns+` FROM nfo_write_preparations WHERE actor_id=$1::uuid AND idempotency_key=$2 AND expires_at>clock_timestamp()`, actor, key).Scan(&result.ID, &result.Version, &requestBytes, &result.Scope.LibraryID, &result.Scope.ItemID, &result.Scope.SourceID, &result.Scope.RootID, &result.Scope.Kind, &result.Scope.Revision, &result.Scope.Generation, &result.Scope.Source.RootPath, &result.Scope.Source.RelativePath, &result.Scope.MediaPath, &result.Scope.DirectoryPath, &maxBytes, &result.Stamp.ModifiedUnixNano, &result.Original, &result.Stamp.SHA256, &result.Replacement, &result.CreatedAt, &result.ExpiresAt)
	if err != nil {
		return domain.NFOWritePreparation{}, storageError(err)
	}
	result.Stamp.Size = int64(len(result.Original))
	result.Stamp.FingerprintVersion = domain.NFOFingerprintVersion
	if json.Unmarshal(requestBytes, &result.Request) != nil || result.Request.MaxBytes != maxBytes || domain.ValidateNFOWritePreparation(result) != nil {
		return domain.NFOWritePreparation{}, domain.ErrDatabase
	}
	actual, _ := domain.NFOWriteRequestDigest(result.Request)
	if actual != digest {
		return domain.NFOWritePreparation{}, domain.ErrDatabase
	}
	return result, nil
}

func (s *Store) FindNFOWritePreparation(ctx context.Context, actor domain.Actor, key, digest string) (domain.NFOWritePreparation, error) {
	if ctx == nil || !validJobKey(key) || len(digest) != 64 {
		return domain.NFOWritePreparation{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	defer tx.Rollback(ctx)
	result, err := readNFOWritePreparation(ctx, tx, actor.UserID, key, digest)
	if err != nil {
		return domain.NFOWritePreparation{}, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.NFOWritePreparation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOWritePreparation{}, storageError(err)
	}
	return result, nil
}

// The caller's observation is re-bound to current authorized catalog data in a
// short transaction. Concurrent equal requests keep the first UUID and bytes.
func (s *Store) SaveNFOWritePreparation(ctx context.Context, actor domain.Actor, key string, input domain.NFOWritePreparation) (domain.NFOWritePreparation, bool, error) {
	if ctx == nil || !validJobKey(key) || domain.ValidateNFOWritePreparation(input) != nil {
		return domain.NFOWritePreparation{}, false, domain.ErrInvalid
	}
	input = domain.CloneNFOWritePreparation(input)
	digest, _ := domain.NFOWriteRequestDigest(input.Request)
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.NFOWritePreparation{}, false, err
	}
	defer tx.Rollback(ctx)
	old, err := readNFOWritePreparation(ctx, tx, actor.UserID, key, digest)
	if err == nil {
		if err = probeAdminStillLive(ctx, tx, actor); err != nil {
			return domain.NFOWritePreparation{}, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.NFOWritePreparation{}, false, storageError(err)
		}
		return old, true, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.NFOWritePreparation{}, false, err
	}
	live, err := readItemNFOScope(ctx, tx, input.Scope.ItemID, input.Scope.Revision)
	if err != nil {
		return domain.NFOWritePreparation{}, false, err
	}
	if live != input.Scope {
		return domain.NFOWritePreparation{}, false, domain.ErrConflict
	}
	// Expiration only reclaims preparation data. There are no filesystem effects
	// or executable jobs attached to these rows in this schema.
	if _, err = tx.Exec(ctx, `DELETE FROM nfo_write_preparations WHERE id IN (SELECT id FROM nfo_write_preparations WHERE expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT 256)`); err != nil {
		return domain.NFOWritePreparation{}, false, storageError(err)
	}
	requestBytes, _ := json.Marshal(input.Request)
	var count, actorCount int64
	var total, library int64
	err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE actor_id=$1::uuid),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),0),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)) FILTER(WHERE library_id=$2::uuid),0) FROM nfo_write_preparations`, actor.UserID, live.LibraryID).Scan(&count, &actorCount, &total, &library)
	if err != nil {
		return domain.NFOWritePreparation{}, false, storageError(err)
	}
	incoming := int64(len(requestBytes) + len(input.Original) + len(input.Replacement))
	if count >= 256 || actorCount >= 32 || total+incoming > 256<<20 || library+incoming > 128<<20 {
		return domain.NFOWritePreparation{}, false, domain.ErrNFOCacheCapacity
	}
	replacementHash := sha256.Sum256(input.Replacement)
	_, err = tx.Exec(ctx, `INSERT INTO nfo_write_preparations(actor_id,idempotency_key,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256) VALUES($1::uuid,$2,$3,$4,$5,$6::uuid,$7::uuid,$8::uuid,$9::uuid,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`, actor.UserID, key, input.Version, requestBytes, digest, live.LibraryID, live.ItemID, live.SourceID, live.RootID, live.Kind, live.Revision, live.Generation, live.Source.RootPath, live.Source.RelativePath, live.MediaPath, live.DirectoryPath, input.Request.MaxBytes, input.Stamp.ModifiedUnixNano, input.Original, input.Stamp.SHA256, input.Replacement, hex.EncodeToString(replacementHash[:]))
	if err != nil {
		return domain.NFOWritePreparation{}, false, storageError(err)
	}
	result, err := readNFOWritePreparation(ctx, tx, actor.UserID, key, digest)
	if err != nil {
		return domain.NFOWritePreparation{}, false, err
	}
	if err = auditAccount(ctx, tx, actor, "nfo.write_prepared", result.ID, nil, map[string]any{"itemId": live.ItemID, "revision": live.Revision, "generation": live.Generation}); err != nil {
		return domain.NFOWritePreparation{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.NFOWritePreparation{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOWritePreparation{}, false, storageError(err)
	}
	return result, false, nil
}
