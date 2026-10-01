package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func deleteProbeRow(ctx context.Context, tx pgx.Tx, c cachedProbe) error {
	leases := int64(0)
	if c.leased {
		leases = 1
	}
	if err := adjustProbeQuota(ctx, tx, c.lease.LibraryID, -1, -c.charge, -leases); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM probe_cache WHERE root_id=$1::uuid AND relative_path=$2`, c.lease.RootID, c.lease.Path)
	return storageError(err)
}
func removeProbeRows(ctx context.Context, tx pgx.Tx, query string, args ...any) (domain.ProbeSweepResult, error) {
	var result domain.ProbeSweepResult
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return result, storageError(err)
	}
	type key struct{ root, path string }
	keys := make([]key, 0, domain.ProbeSweepMax)
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.root, &k.path); err != nil {
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
		c, err := readCachedProbe(ctx, tx, k.root, k.path)
		if err != nil {
			return result, err
		}
		if err = deleteProbeRow(ctx, tx, c); err != nil {
			return result, err
		}
		result.Deleted++
		result.FreedBytes += c.charge
		if c.leased {
			result.ReleasedLeases++
		}
	}
	return result, nil
}
func releaseExpiredProbeLeases(ctx context.Context, tx pgx.Tx, limit int) (domain.ProbeSweepResult, error) {
	return removeProbeRows(ctx, tx, `SELECT c.root_id::text,c.relative_path FROM probe_cache c JOIN jobs j ON j.id=c.lease_job_id WHERE c.lease_owner IS NOT NULL AND (c.lease_until<=clock_timestamp() OR j.state<>'running' OR j.generation<>c.lease_job_generation OR j.owner<>c.lease_owner OR j.lease_until<=clock_timestamp() OR j.cancel_requested) ORDER BY c.root_id,c.relative_path LIMIT $1`, limit)
}
func releaseParentProbeLeases(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := removeProbeRows(ctx, tx, `SELECT root_id::text,relative_path FROM probe_cache WHERE lease_job_id=$1::uuid ORDER BY root_id,relative_path LIMIT 8`, id)
	return err
}
func probeCapacityFits(ctx context.Context, tx pgx.Tx, library string, rows, bytes int64) (bool, bool, error) {
	var capacity, slot bool
	err := tx.QueryRow(ctx, `SELECT g.rows_used+$2<=g.global_row_limit AND g.bytes_used+$3<=g.global_byte_limit AND q.rows_used+$2<=q.row_limit AND q.bytes_used+$3<=q.byte_limit,g.active_leases<g.active_lease_limit FROM probe_cache_quota g JOIN probe_library_quota q ON q.library_id=$1::uuid WHERE g.singleton`, library, rows, bytes).Scan(&capacity, &slot)
	return capacity, slot, storageError(err)
}

const probeExpiredPageSQL = `SELECT c.root_id::text,c.relative_path FROM probe_cache c WHERE c.lease_owner IS NULL AND c.expires_at<=CURRENT_TIMESTAMP AND NOT (c.root_id=$1::uuid AND c.relative_path=$2) ORDER BY c.expires_at,c.root_id,c.relative_path LIMIT $3`
const probeLRUPageSQL = `SELECT c.root_id::text,c.relative_path FROM probe_cache c WHERE c.lease_owner IS NULL AND NOT (c.root_id=$1::uuid AND c.relative_path=$2) ORDER BY c.last_used_at,c.root_id,c.relative_path LIMIT $3`
const probeLibraryExpiredPageSQL = `SELECT c.root_id::text,c.relative_path FROM probe_cache c WHERE c.lease_owner IS NULL AND c.library_id=$4::uuid AND c.expires_at<=CURRENT_TIMESTAMP AND NOT (c.root_id=$1::uuid AND c.relative_path=$2) ORDER BY c.expires_at,c.root_id,c.relative_path LIMIT $3`
const probeLibraryLRUPageSQL = `SELECT c.root_id::text,c.relative_path FROM probe_cache c WHERE c.lease_owner IS NULL AND c.library_id=$4::uuid AND NOT (c.root_id=$1::uuid AND c.relative_path=$2) ORDER BY c.last_used_at,c.root_id,c.relative_path LIMIT $3`
const probeSweepPendingSQL = `SELECT c.root_id::text,c.relative_path FROM probe_cache c WHERE c.lease_owner IS NULL AND c.expires_at IS NULL ORDER BY c.expires_at,c.root_id,c.relative_path LIMIT $1`
const probeSweepExpiredSQL = `SELECT c.root_id::text,c.relative_path FROM probe_cache c WHERE c.lease_owner IS NULL AND c.expires_at<=CURRENT_TIMESTAMP ORDER BY c.expires_at,c.root_id,c.relative_path LIMIT $1`

func makeProbeCapacity(ctx context.Context, tx pgx.Tx, library string, rows, bytes int64, root, path string) error {
	fits, slot, err := probeCapacityFits(ctx, tx, library, rows, bytes)
	if err != nil {
		return err
	}
	if !slot {
		return domain.ErrProbeBusy
	}
	if fits {
		return nil
	}
	// Choose the constrained scope before reading cache rows. Both pages use
	// partial indexes, without sorting the entire global cache. CURRENT_TIMESTAMP
	// is stable for an index range; slightly late expiry cleanup is safe.
	var libraryFits bool
	if err = tx.QueryRow(ctx, `SELECT rows_used+$2<=row_limit AND bytes_used+$3<=byte_limit FROM probe_library_quota WHERE library_id=$1::uuid`, library, rows, bytes).Scan(&libraryFits); err != nil {
		return storageError(err)
	}
	expiredSQL, lruSQL := probeExpiredPageSQL, probeLRUPageSQL
	args := []any{root, path, domain.ProbeSweepMax}
	if !libraryFits {
		expiredSQL, lruSQL = probeLibraryExpiredPageSQL, probeLibraryLRUPageSQL
		args = append(args, library)
	}
	removed, err := removeProbeRows(ctx, tx, expiredSQL, args...)
	if err != nil {
		return err
	}
	fits, slot, err = probeCapacityFits(ctx, tx, library, rows, bytes)
	if err != nil {
		return err
	}
	if !fits && removed.Deleted < domain.ProbeSweepMax {
		args[2] = domain.ProbeSweepMax - int(removed.Deleted)
		if _, err = removeProbeRows(ctx, tx, lruSQL, args...); err != nil {
			return err
		}
	}
	fits, slot, err = probeCapacityFits(ctx, tx, library, rows, bytes)
	if err != nil {
		return err
	}
	if !slot {
		return domain.ErrProbeBusy
	}
	if !fits {
		return domain.ErrProbeCacheCapacity
	}
	return nil
}
func (s *Store) ReleaseProbeLease(parent context.Context, l domain.JobLease, lease domain.ProbeLease) error {
	if err := domain.ValidateProbeLease(lease); err != nil {
		return err
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	if _, err = readProbePolicy(ctx, tx); err != nil {
		return err
	}
	if _, err = fencedJob(ctx, tx, l); err != nil {
		return err
	}
	c, err := readCachedProbe(ctx, tx, lease.RootID, lease.Path)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrProbeLeaseLost
	}
	if err != nil {
		return err
	}
	if !c.leased || c.lease.Generation != lease.Generation || c.lease.Owner != l.Owner || c.lease.JobID != l.Job.ID || c.lease.JobGeneration != l.Generation || lease.Owner != l.Owner || lease.JobID != l.Job.ID || lease.JobGeneration != l.Generation {
		return domain.ErrProbeLeaseLost
	}
	if err = deleteProbeRow(ctx, tx, c); err != nil {
		return err
	}
	if err = guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
func (s *Store) SweepProbeCache(parent context.Context, limit int) (domain.ProbeSweepResult, error) {
	if limit < 1 || limit > domain.ProbeSweepMax {
		return domain.ProbeSweepResult{}, domain.ErrInvalid
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.ProbeSweepResult{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	if _, err = readProbePolicy(ctx, tx); err != nil {
		return domain.ProbeSweepResult{}, err
	}
	result, err := releaseExpiredProbeLeases(ctx, tx, limit)
	if err != nil {
		return domain.ProbeSweepResult{}, err
	}
	for _, query := range []string{probeSweepPendingSQL, probeSweepExpiredSQL} {
		remaining := limit - int(result.Deleted)
		if remaining == 0 {
			break
		}
		next, e := removeProbeRows(ctx, tx, query, remaining)
		if e != nil {
			return domain.ProbeSweepResult{}, e
		}
		result.Deleted += next.Deleted
		result.FreedBytes += next.FreedBytes
	}
	// Identity/scope cardinalities have small independent hard bounds. Only
	// unreferenced rows can be removed; a retained phase preserves its identity.
	tag, err := tx.Exec(ctx, `DELETE FROM tool_versions t WHERE NOT EXISTS(SELECT 1 FROM probe_cache c WHERE c.tool_version_id=t.id) AND NOT EXISTS(SELECT 1 FROM probe_job_state p WHERE p.tool_version_id=t.id) AND NOT EXISTS(SELECT 1 FROM probe_requests r WHERE r.tool_version_id=t.id)`)
	if err != nil {
		return domain.ProbeSweepResult{}, storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE probe_cache_quota SET tools_used=tools_used-$1 WHERE singleton`, tag.RowsAffected()); err != nil {
		return domain.ProbeSweepResult{}, storageError(err)
	}
	tag, err = tx.Exec(ctx, `DELETE FROM probe_library_quota q WHERE q.rows_used=0 AND NOT EXISTS(SELECT 1 FROM probe_job_state p JOIN jobs j ON j.id=p.job_id WHERE p.library_id=q.library_id AND p.phase='running' AND j.state IN ('queued','running')) AND NOT EXISTS(SELECT 1 FROM probe_requests r JOIN jobs j ON j.id=r.job_id WHERE r.library_id=q.library_id AND j.state IN ('queued','running'))`)
	if err != nil {
		return domain.ProbeSweepResult{}, storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE probe_cache_quota SET library_scopes=library_scopes-$1 WHERE singleton`, tag.RowsAffected()); err != nil {
		return domain.ProbeSweepResult{}, storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProbeSweepResult{}, storageError(err)
	}
	return result, nil
}
func (s *Store) InvalidateProbeLibrary(ctx context.Context, a domain.Actor, id string) error {
	return s.invalidateProbe(ctx, a, id, false)
}
func (s *Store) InvalidateProbeItem(ctx context.Context, a domain.Actor, id string) error {
	return s.invalidateProbe(ctx, a, id, true)
}
func (s *Store) invalidateProbe(parent context.Context, a domain.Actor, id string, item bool) error {
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	if parent == nil {
		return domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='1500ms'; SET LOCAL statement_timeout='2000ms'`); err != nil {
		return storageError(err)
	}
	table := "libraries"
	if item {
		table = "items"
	}
	tag, err := tx.Exec(ctx, `UPDATE `+table+` SET probe_generation=probe_generation+1 WHERE id=$1::uuid`, id)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrNotFound
	}
	event := "probe.library_invalidated"
	if item {
		event = "probe.item_invalidated"
	}
	if err = auditAccount(ctx, tx, a, event, id, nil, nil); err != nil {
		return err
	}
	// The authorization rows stay locked, but wall-clock expiry can still pass
	// while this transaction waits or writes. Reject that case before commit.
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
