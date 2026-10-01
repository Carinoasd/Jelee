package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.NFORepository = (*Store)(nil)
var _ app.NFOAdminRepository = (*Store)(nil)

const nfoPolicyColumns = `global_row_limit,global_byte_limit,library_row_limit,library_byte_limit,library_limit,positive_ttl_seconds,negative_ttl_seconds`

func readNFOCachePolicy(ctx context.Context, tx pgx.Tx) (domain.NFOCachePolicy, error) {
	var p domain.NFOCachePolicy
	var positive, negative int64
	err := tx.QueryRow(ctx, `SELECT `+nfoPolicyColumns+` FROM nfo_cache_quota WHERE singleton FOR UPDATE`).Scan(&p.MaxRows, &p.MaxBytes, &p.LibraryMaxRows, &p.LibraryMaxBytes, &p.MaxLibraries, &positive, &negative)
	p.PositiveTTL = time.Duration(positive) * time.Second
	p.NegativeTTL = time.Duration(negative) * time.Second
	return p, storageError(err)
}
func (s *Store) EnsureNFOCachePolicy(parent context.Context, p domain.NFOCachePolicy) error {
	if err := domain.ValidateNFOCachePolicy(p); err != nil {
		return err
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO nfo_cache_quota(singleton,`+nfoPolicyColumns+`) VALUES(true,$1,$2,$3,$4,$5,$6,$7) ON CONFLICT(singleton) DO NOTHING`, p.MaxRows, p.MaxBytes, p.LibraryMaxRows, p.LibraryMaxBytes, p.MaxLibraries, int64(p.PositiveTTL/time.Second), int64(p.NegativeTTL/time.Second))
	if err != nil {
		return storageError(err)
	}
	existing, err := readNFOCachePolicy(ctx, tx)
	if err != nil {
		return err
	}
	if existing != p {
		return domain.ErrConflict
	}
	return storageError(tx.Commit(ctx))
}
func readNFOLibraryPolicy(ctx context.Context, tx pgx.Tx, id string) (domain.NFOLibraryPolicy, error) {
	var p domain.NFOLibraryPolicy
	err := tx.QueryRow(ctx, `SELECT id::text,nfo_mode,nfo_generation FROM libraries WHERE id=$1::uuid FOR UPDATE`, id).Scan(&p.LibraryID, &p.Mode, &p.Generation)
	return p, storageError(err)
}
func (s *Store) GetNFOLibraryPolicy(parent context.Context, a domain.Actor, id string) (domain.NFOLibraryPolicy, error) {
	if parent == nil {
		return domain.NFOLibraryPolicy{}, domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return domain.NFOLibraryPolicy{}, domain.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.NFOLibraryPolicy{}, err
	}
	defer tx.Rollback(ctx)
	p, err := readNFOLibraryPolicy(ctx, tx, id)
	if err != nil {
		return domain.NFOLibraryPolicy{}, err
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.NFOLibraryPolicy{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOLibraryPolicy{}, storageError(err)
	}
	return p, nil
}

// This maintenance changes only generated ledger records. An expired exact
// key is reclaimed first; both deletes together remain within the caller bound.
func cleanupNFOPolicyRequests(ctx context.Context, tx pgx.Tx, limit int, actor, key string) (int64, error) {
	var deleted int64
	if actor != "" {
		tag, err := tx.Exec(ctx, `DELETE FROM nfo_policy_requests WHERE actor_id=$1::uuid AND idempotency_key=$2 AND expires_at<=clock_timestamp()`, actor, key)
		if err != nil {
			return 0, storageError(err)
		}
		deleted = tag.RowsAffected()
	}
	if int64(limit) > deleted {
		tag, err := tx.Exec(ctx, `DELETE FROM nfo_policy_requests WHERE (actor_id,idempotency_key) IN (SELECT actor_id,idempotency_key FROM nfo_policy_requests WHERE expires_at<=CURRENT_TIMESTAMP ORDER BY expires_at,actor_id,idempotency_key LIMIT $1)`, int64(limit)-deleted)
		if err != nil {
			return 0, storageError(err)
		}
		deleted += tag.RowsAffected()
	}
	return deleted, nil
}
func (s *Store) SetNFOLibraryPolicy(parent context.Context, a domain.Actor, id, key string, expected int64, mode string) (domain.NFOLibraryPolicy, bool, error) {
	if parent == nil || !validJobKey(key) || expected < 1 || (mode != domain.NFOModeOff && mode != domain.NFOModeReadOnly) {
		return domain.NFOLibraryPolicy{}, false, domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return domain.NFOLibraryPolicy{}, false, domain.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.NFOLibraryPolicy{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err = cleanupNFOPolicyRequests(ctx, tx, domain.NFOSweepMax, a.UserID, key); err != nil {
		return domain.NFOLibraryPolicy{}, false, err
	}
	var old domain.NFOLibraryPolicy
	var oldExpected int64
	err = tx.QueryRow(ctx, `SELECT library_id::text,requested_mode,expected_generation,result_generation FROM nfo_policy_requests WHERE actor_id=$1::uuid AND idempotency_key=$2`, a.UserID, key).Scan(&old.LibraryID, &old.Mode, &oldExpected, &old.Generation)
	if err == nil {
		if old.LibraryID != id || old.Mode != mode || oldExpected != expected {
			return domain.NFOLibraryPolicy{}, false, domain.ErrConflict
		}
		if err = probeAdminStillLive(ctx, tx, a); err != nil {
			return domain.NFOLibraryPolicy{}, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.NFOLibraryPolicy{}, false, storageError(err)
		}
		return old, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.NFOLibraryPolicy{}, false, storageError(err)
	}
	current, err := readNFOLibraryPolicy(ctx, tx, id)
	if err != nil {
		return domain.NFOLibraryPolicy{}, false, err
	}
	if current.Generation != expected {
		return domain.NFOLibraryPolicy{}, false, domain.ErrConflict
	}
	var total, actorCount int
	if err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE actor_id=$1::uuid) FROM nfo_policy_requests`, a.UserID).Scan(&total, &actorCount); err != nil {
		return domain.NFOLibraryPolicy{}, false, storageError(err)
	}
	if total >= domain.NFORequestGlobalMax || actorCount >= domain.NFORequestActorMax {
		return domain.NFOLibraryPolicy{}, false, domain.ErrNFOCacheCapacity
	}
	result := current
	if current.Mode != mode {
		if err = tx.QueryRow(ctx, `UPDATE libraries SET nfo_mode=$2,nfo_generation=nfo_generation+1 WHERE id=$1::uuid AND nfo_generation=$3 RETURNING id::text,nfo_mode,nfo_generation`, id, mode, expected).Scan(&result.LibraryID, &result.Mode, &result.Generation); err != nil {
			return domain.NFOLibraryPolicy{}, false, storageError(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO nfo_policy_requests(actor_id,idempotency_key,library_id,requested_mode,expected_generation,result_generation) VALUES($1::uuid,$2,$3::uuid,$4,$5,$6)`, a.UserID, key, id, mode, expected, result.Generation); err != nil {
		return domain.NFOLibraryPolicy{}, false, storageError(err)
	}
	if err = auditAccount(ctx, tx, a, "nfo.policy_changed", id, current, result); err != nil {
		return domain.NFOLibraryPolicy{}, false, err
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.NFOLibraryPolicy{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOLibraryPolicy{}, false, storageError(err)
	}
	return result, false, nil
}
func ensureNFOLibrary(ctx context.Context, tx pgx.Tx, id string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nfo_library_quota WHERE library_id=$1::uuid)`, id).Scan(&exists); err != nil {
		return storageError(err)
	}
	if exists {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE nfo_cache_quota SET library_scopes=library_scopes+1 WHERE singleton AND library_scopes<library_limit`)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrNFOCacheCapacity
	}
	_, err = tx.Exec(ctx, `INSERT INTO nfo_library_quota(library_id,row_limit,byte_limit) SELECT $1::uuid,library_row_limit,library_byte_limit FROM nfo_cache_quota WHERE singleton`, id)
	return storageError(err)
}
func adjustNFOQuota(ctx context.Context, tx pgx.Tx, id string, rows, bytes int64) error {
	for _, query := range []string{`UPDATE nfo_cache_quota SET rows_used=rows_used+$2,bytes_used=bytes_used+$3 WHERE singleton AND $1::uuid IS NOT NULL`, `UPDATE nfo_library_quota SET rows_used=rows_used+$2,bytes_used=bytes_used+$3 WHERE library_id=$1::uuid`} {
		tag, err := tx.Exec(ctx, query, id, rows, bytes)
		if err != nil {
			return storageError(err)
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrNFOCacheCapacity
		}
	}
	return nil
}
