package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

type cachedProbe struct {
	lease                     domain.ProbeLease
	state, errorCode          string
	charge                    int64
	failures                  int
	valid, leased, leaseValid bool
	expiry                    time.Time
}

func readCachedProbe(ctx context.Context, tx pgx.Tx, root, path string) (cachedProbe, error) {
	var c cachedProbe
	err := tx.QueryRow(ctx, `SELECT root_id::text,relative_path,library_id::text,COALESCE(item_id::text,''),size,modified_unix_nano,encode(fingerprint,'hex'),fingerprint_version,tool_version_id::text,library_generation,root_generation,COALESCE(item_generation,0),state,error_code,charge_bytes,failure_count,COALESCE(expires_at>clock_timestamp() AND (retry_after IS NULL OR retry_after>clock_timestamp()),false),COALESCE(expires_at,'epoch'),COALESCE(lease_owner,''),COALESCE(lease_generation,0),COALESCE(lease_until,'epoch'),COALESCE(lease_job_id::text,''),COALESCE(lease_job_generation,0),lease_owner IS NOT NULL,COALESCE(lease_until>clock_timestamp(),false) FROM probe_cache WHERE root_id=$1::uuid AND relative_path=$2 FOR UPDATE`, root, path).Scan(&c.lease.RootID, &c.lease.Path, &c.lease.LibraryID, &c.lease.ItemID, &c.lease.Stamp.Size, &c.lease.Stamp.ModifiedUnixNano, &c.lease.Stamp.Fingerprint, &c.lease.Stamp.FingerprintVersion, &c.lease.Identity.ID, &c.lease.LibraryGeneration, &c.lease.RootGeneration, &c.lease.ItemGeneration, &c.state, &c.errorCode, &c.charge, &c.failures, &c.valid, &c.expiry, &c.lease.Owner, &c.lease.Generation, &c.lease.ExpiresAt, &c.lease.JobID, &c.lease.JobGeneration, &c.leased, &c.leaseValid)
	return c, storageError(err)
}
func cacheMatches(c cachedProbe, p domain.ProbePhase, e probeEntry, stamp domain.ProbeStamp) bool {
	return c.lease.LibraryID == p.LibraryID && c.lease.Stamp == stamp && c.lease.Identity.ID == p.Start.Identity.ID && c.lease.LibraryGeneration == p.LibraryGeneration && c.lease.RootGeneration == e.rootGeneration && c.lease.ItemID == e.itemID && c.lease.ItemGeneration == e.itemGeneration
}
func lookupKind(c cachedProbe, p domain.ProbePhase, e probeEntry, stamp domain.ProbeStamp) string {
	if c.leased && c.leaseValid {
		return domain.ProbeLookupBusy
	}
	if !c.valid || !cacheMatches(c, p, e, stamp) {
		return domain.ProbeLookupMiss
	}
	if c.state == "ready" {
		return domain.ProbeLookupHit
	}
	if c.state == "failed" {
		return domain.ProbeLookupNegativeHit
	}
	return domain.ProbeLookupMiss
}
func (s *Store) LookupProbeBatch(parent context.Context, l domain.JobLease, token domain.ProbePageToken, candidates []domain.ProbeCandidate) ([]domain.ProbeLookup, error) {
	if err := domain.ValidateProbeCandidateBatch(candidates); err != nil {
		return nil, err
	}
	if err := domain.ValidateProbePageToken(token); err != nil {
		return nil, err
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedProbePhase(ctx, tx, l, false)
	if err != nil {
		return nil, err
	}
	entries, err := checkProbePrefix(ctx, tx, p, token, candidates, true)
	if err != nil {
		return nil, err
	}
	results := make([]domain.ProbeLookup, 0, len(candidates))
	for i, e := range entries {
		c, err := readCachedProbe(ctx, tx, e.Inventory.RootID, e.Inventory.Path)
		kind := domain.ProbeLookupMiss
		if err == nil {
			kind = lookupKind(c, p, e, candidates[i].Stamp) //nolint:gosec // G602: checkProbePrefix returns exactly len(candidates) entries
		} else if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
		results = append(results, domain.ProbeLookup{InventoryID: e.Inventory.ID, Kind: kind})
	}
	if err = guardProbeParent(ctx, tx, l); err != nil {
		return nil, err
	}
	return results, storageError(tx.Commit(ctx))
}
func (s *Store) AcquireProbe(parent context.Context, l domain.JobLease, token domain.ProbePageToken, candidate domain.ProbeCandidate) (domain.ProbeLease, error) {
	if err := domain.ValidateProbeCandidate(candidate); err != nil {
		return domain.ProbeLease{}, err
	}
	if err := domain.ValidateProbePageToken(token); err != nil {
		return domain.ProbeLease{}, err
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.ProbeLease{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedProbePhase(ctx, tx, l, false)
	if err != nil {
		return domain.ProbeLease{}, err
	}
	entries, err := checkProbePrefix(ctx, tx, p, token, []domain.ProbeCandidate{candidate}, true)
	if err != nil {
		return domain.ProbeLease{}, err
	}
	e := entries[0]
	if _, err = releaseExpiredProbeLeases(ctx, tx, domain.ProbeSweepMax); err != nil {
		return domain.ProbeLease{}, err
	}
	var held bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM probe_cache WHERE lease_job_id=$1::uuid)`, l.Job.ID).Scan(&held); err != nil {
		return domain.ProbeLease{}, storageError(err)
	}
	if held {
		return domain.ProbeLease{}, domain.ErrProbeBusy
	}
	existing, readErr := readCachedProbe(ctx, tx, e.Inventory.RootID, e.Inventory.Path)
	oldCharge := int64(0)
	rows := int64(1)
	failures := 0
	if readErr == nil {
		kind := lookupKind(existing, p, e, candidate.Stamp)
		if kind == domain.ProbeLookupBusy {
			return domain.ProbeLease{}, domain.ErrProbeBusy
		}
		if kind == domain.ProbeLookupHit || kind == domain.ProbeLookupNegativeHit {
			return domain.ProbeLease{}, domain.ErrConflict
		}
		oldCharge = existing.charge
		rows = 0
		if cacheMatches(existing, p, e, candidate.Stamp) {
			failures = existing.failures
		}
	} else if !errors.Is(readErr, domain.ErrNotFound) {
		return domain.ProbeLease{}, readErr
	}
	charge := int64(domain.ProbeRowAllowanceBytes + domain.ProbeMetadataMaxBytes)
	if err = makeProbeCapacity(ctx, tx, p.LibraryID, rows, charge-oldCharge, e.Inventory.RootID, e.Inventory.Path); err != nil {
		return domain.ProbeLease{}, err
	}
	if err = adjustProbeQuota(ctx, tx, p.LibraryID, rows, charge-oldCharge, 1); err != nil {
		return domain.ProbeLease{}, err
	}
	var expires time.Time
	var generation int64
	err = tx.QueryRow(ctx, `SELECT nextval('probe_lease_fence_seq'),LEAST(j.lease_until,clock_timestamp()+q.lease_seconds*interval '1 second') FROM jobs j CROSS JOIN probe_cache_quota q WHERE j.id=$1::uuid AND q.singleton`, l.Job.ID).Scan(&generation, &expires)
	if err != nil {
		return domain.ProbeLease{}, storageError(err)
	}
	lease := domain.ProbeLease{InventoryID: e.Inventory.ID, RootID: e.Inventory.RootID, Path: e.Inventory.Path, LibraryID: p.LibraryID, ItemID: e.itemID, LibraryGeneration: p.LibraryGeneration, RootGeneration: e.rootGeneration, ItemGeneration: e.itemGeneration, Identity: p.Start.Identity, Stamp: candidate.Stamp, Owner: l.Owner, Generation: generation, JobID: l.Job.ID, JobGeneration: l.Generation, ExpiresAt: expires}
	_, err = tx.Exec(ctx, `INSERT INTO probe_cache(root_id,relative_path,library_id,item_id,size,modified_unix_nano,fingerprint,fingerprint_version,tool_version_id,library_generation,root_generation,item_generation,state,failure_count,lease_owner,lease_generation,lease_until,lease_job_id,lease_job_generation,charge_bytes) VALUES($1::uuid,$2,$3::uuid,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9::uuid,$10,$11,NULLIF($12,0),'pending',$13,$14,$15,$16,$17::uuid,$18,$19) ON CONFLICT(root_id,relative_path) DO UPDATE SET item_id=EXCLUDED.item_id,size=EXCLUDED.size,modified_unix_nano=EXCLUDED.modified_unix_nano,fingerprint=EXCLUDED.fingerprint,fingerprint_version=EXCLUDED.fingerprint_version,tool_version_id=EXCLUDED.tool_version_id,library_generation=EXCLUDED.library_generation,root_generation=EXCLUDED.root_generation,item_generation=EXCLUDED.item_generation,state='pending',metadata=NULL,error_code='',expires_at=NULL,retry_after=NULL,failure_count=EXCLUDED.failure_count,lease_owner=EXCLUDED.lease_owner,lease_generation=EXCLUDED.lease_generation,lease_until=EXCLUDED.lease_until,lease_job_id=EXCLUDED.lease_job_id,lease_job_generation=EXCLUDED.lease_job_generation,charge_bytes=EXCLUDED.charge_bytes,updated_at=clock_timestamp()`, lease.RootID, lease.Path, lease.LibraryID, lease.ItemID, lease.Stamp.Size, lease.Stamp.ModifiedUnixNano, probeBytes(lease.Stamp.Fingerprint), lease.Stamp.FingerprintVersion, lease.Identity.ID, lease.LibraryGeneration, lease.RootGeneration, lease.ItemGeneration, failures, lease.Owner, lease.Generation, lease.ExpiresAt, lease.JobID, lease.JobGeneration, charge)
	if err != nil {
		return domain.ProbeLease{}, storageError(err)
	}
	if err = guardProbeParentExpiry(ctx, tx, l, expires); err != nil {
		return domain.ProbeLease{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProbeLease{}, storageError(err)
	}
	return lease, nil
}
func guardProbeParentExpiry(ctx context.Context, tx pgx.Tx, l domain.JobLease, expiry time.Time) error {
	err := guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid AND NOT cancel_requested AND clock_timestamp()<$2`, l.Job.ID, expiry)
	if errors.Is(err, domain.ErrJobLeaseLost) {
		return domain.ErrProbeLeaseLost
	}
	return err
}
func validateCurrentProbeLease(c cachedProbe, l domain.JobLease, p domain.ProbePhase, e probeEntry, want domain.ProbeLease) error {
	// ExpiresAt is deliberately not compared with a caller's old timestamp.
	if !c.leased || !c.leaseValid || c.lease.Owner != l.Owner || c.lease.JobID != l.Job.ID || c.lease.JobGeneration != l.Generation || c.lease.Generation != want.Generation || want.Owner != l.Owner || want.JobID != l.Job.ID || want.JobGeneration != l.Generation || want.InventoryID != e.Inventory.ID || want.RootID != e.Inventory.RootID || want.Path != e.Inventory.Path || want.Identity != p.Start.Identity || want.LibraryID != p.LibraryID || want.ItemID != e.itemID || want.LibraryGeneration != p.LibraryGeneration || want.RootGeneration != e.rootGeneration || want.ItemGeneration != e.itemGeneration || !cacheMatches(c, p, e, want.Stamp) {
		return domain.ErrProbeLeaseLost
	}
	return nil
}
