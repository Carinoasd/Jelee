package postgres

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

const nfoExpiredGlobalSQL = `SELECT root_id::text,relative_path,library_id::text,charge_bytes FROM nfo_cache WHERE expires_at<=CURRENT_TIMESTAMP ORDER BY expires_at,root_id,relative_path LIMIT $1`
const nfoLRUGlobalSQL = `SELECT root_id::text,relative_path,library_id::text,charge_bytes FROM nfo_cache ORDER BY last_used_at,root_id,relative_path LIMIT $1`
const nfoExpiredLibrarySQL = `SELECT root_id::text,relative_path,library_id::text,charge_bytes FROM nfo_cache WHERE library_id=$1::uuid AND expires_at<=CURRENT_TIMESTAMP ORDER BY expires_at,root_id,relative_path LIMIT $2`
const nfoLRULibrarySQL = `SELECT root_id::text,relative_path,library_id::text,charge_bytes FROM nfo_cache WHERE library_id=$1::uuid ORDER BY last_used_at,root_id,relative_path LIMIT $2`

func removeNFORows(ctx context.Context, tx pgx.Tx, skipRoot, skipPath, query string, args ...any) (domain.NFOSweepResult, error) {
	result := domain.NFOSweepResult{}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return result, storageError(err)
	}
	type row struct {
		root, path, library string
		charge              int64
	}
	keys := make([]row, 0, domain.NFOSweepMax)
	for rows.Next() {
		var k row
		if err = rows.Scan(&k.root, &k.path, &k.library, &k.charge); err != nil {
			rows.Close()
			return result, storageError(err)
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, storageError(err)
	}
	for _, k := range keys {
		if k.root == skipRoot && k.path == skipPath {
			continue
		}
		if _, err = tx.Exec(ctx, `DELETE FROM nfo_cache WHERE root_id=$1::uuid AND relative_path=$2`, k.root, k.path); err != nil {
			return result, storageError(err)
		}
		if err = adjustNFOQuota(ctx, tx, k.library, -1, -k.charge); err != nil {
			return result, err
		}
		result.Deleted++
		result.FreedBytes += k.charge
	}
	return result, nil
}
func nfoCapacityFits(ctx context.Context, tx pgx.Tx, library string, rows, bytes int64) (bool, bool, error) {
	var global, local bool
	err := tx.QueryRow(ctx, `SELECT g.rows_used+$2<=g.global_row_limit AND g.bytes_used+$3<=g.global_byte_limit,q.rows_used+$2<=q.row_limit AND q.bytes_used+$3<=q.byte_limit FROM nfo_cache_quota g JOIN nfo_library_quota q ON q.library_id=$1::uuid WHERE g.singleton`, library, rows, bytes).Scan(&global, &local)
	return global, local, storageError(err)
}
func makeNFOCapacity(ctx context.Context, tx pgx.Tx, library string, rows, bytes int64, root, path string) error {
	global, local, err := nfoCapacityFits(ctx, tx, library, rows, bytes)
	if err != nil {
		return err
	}
	if global && local {
		return nil
	}
	// Pick the constrained scope before reading bounded index pages. No scan or
	// sort across the entire cache is needed. An updated row is never evicted.
	expired, lru := nfoExpiredGlobalSQL, nfoLRUGlobalSQL
	args := []any{domain.NFOSweepMax}
	limitArg := 0
	if !local {
		expired, lru = nfoExpiredLibrarySQL, nfoLRULibrarySQL
		args = []any{library, domain.NFOSweepMax}
		limitArg = 1
	}
	removed, err := removeNFORows(ctx, tx, root, path, expired, args...)
	if err != nil {
		return err
	}
	global, local, err = nfoCapacityFits(ctx, tx, library, rows, bytes)
	if err != nil {
		return err
	}
	if !(global && local) && removed.Deleted < domain.NFOSweepMax {
		args[limitArg] = domain.NFOSweepMax - int(removed.Deleted)
		if _, err = removeNFORows(ctx, tx, root, path, lru, args...); err != nil {
			return err
		}
	}
	global, local, err = nfoCapacityFits(ctx, tx, library, rows, bytes)
	if err != nil {
		return err
	}
	if !global || !local {
		return domain.ErrNFOCacheCapacity
	}
	return nil
}
func (s *Store) SweepNFOCache(parent context.Context, limit int) (domain.NFOSweepResult, error) {
	if limit < 1 || limit > domain.NFOSweepMax {
		return domain.NFOSweepResult{}, domain.ErrInvalid
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.NFOSweepResult{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	if _, err = readNFOCachePolicy(ctx, tx); err != nil {
		return domain.NFOSweepResult{}, err
	}
	result, err := removeNFORows(ctx, tx, "", "", nfoExpiredGlobalSQL, limit)
	if err != nil {
		return domain.NFOSweepResult{}, err
	}
	remaining := limit - int(result.Deleted)
	if remaining > 0 {
		result.DeletedRequests, err = cleanupNFOPolicyRequests(ctx, tx, remaining, "", "")
		if err != nil {
			return domain.NFOSweepResult{}, err
		}
		remaining -= int(result.DeletedRequests)
	}
	if remaining > 0 {
		tag, e := tx.Exec(ctx, `DELETE FROM nfo_library_quota WHERE library_id IN (SELECT q.library_id FROM nfo_library_quota q WHERE q.rows_used=0 AND NOT EXISTS(SELECT 1 FROM nfo_job_state p JOIN jobs j ON j.id=p.job_id WHERE p.library_id=q.library_id AND p.mode='read-only' AND j.state IN ('queued','running')) ORDER BY q.library_id LIMIT $1)`, remaining)
		if e != nil {
			return domain.NFOSweepResult{}, storageError(e)
		}
		if _, err = tx.Exec(ctx, `UPDATE nfo_cache_quota SET library_scopes=library_scopes-$1 WHERE singleton`, tag.RowsAffected()); err != nil {
			return domain.NFOSweepResult{}, storageError(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.NFOSweepResult{}, storageError(err)
	}
	return result, nil
}
