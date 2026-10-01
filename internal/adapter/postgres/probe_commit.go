package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func (s *Store) CommitProbeBatch(parent context.Context, l domain.JobLease, token domain.ProbePageToken, completions []domain.ProbeCompletion) (domain.ProbePhase, error) {
	if err := domain.ValidateProbeCompletionBatch(completions); err != nil {
		return domain.ProbePhase{}, err
	}
	if err := domain.ValidateProbePageToken(token); err != nil {
		return domain.ProbePhase{}, err
	}
	candidates := make([]domain.ProbeCandidate, 0, len(completions))
	payloads := make([][]byte, len(completions))
	for i, c := range completions {
		candidates = append(candidates, c.Candidate)
		if c.Metadata != nil {
			var err error
			payloads[i], err = domain.MarshalProbeMetadata(*c.Metadata)
			if err != nil {
				return domain.ProbePhase{}, err
			}
		}
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedProbePhase(ctx, tx, l, false)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	requireStamp := completions[0].Kind != domain.ProbeCompletionChanged && completions[0].Kind != domain.ProbeCompletionUnavailable
	entries, err := checkProbePrefix(ctx, tx, p, token, candidates, requireStamp)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	policy, err := readProbePolicy(ctx, tx)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	var progress domain.ProbeProgress
	var latestAllowed time.Time
	for i, completion := range completions {
		e := entries[i]
		var cached cachedProbe
		hit := completion.Kind == domain.ProbeCompletionHit || completion.Kind == domain.ProbeCompletionNegativeHit
		if hit || completion.Lease != nil {
			cached, err = readCachedProbe(ctx, tx, e.Inventory.RootID, e.Inventory.Path)
			if errors.Is(err, domain.ErrNotFound) {
				return domain.ProbePhase{}, domain.ErrProbeLeaseLost
			}
			if err != nil {
				return domain.ProbePhase{}, err
			}
			if hit {
				if lookupKind(cached, p, e, completion.Candidate.Stamp) != completion.Kind {
					return domain.ProbePhase{}, domain.ErrConflict
				}
				if latestAllowed.IsZero() || cached.expiry.Before(latestAllowed) {
					latestAllowed = cached.expiry
				}
			} else {
				if err = validateCurrentProbeLease(cached, l, p, e, *completion.Lease); err != nil {
					return domain.ProbePhase{}, err
				}
				latestAllowed = cached.lease.ExpiresAt
			}
		}
		if completion.Kind == domain.ProbeCompletionSucceeded {
			var metadataBytes int64
			if err = tx.QueryRow(ctx, `SELECT octet_length($1::jsonb::text)`, string(payloads[i])).Scan(&metadataBytes); err != nil {
				return domain.ProbePhase{}, storageError(err)
			}
			if metadataBytes > domain.ProbeMetadataMaxBytes {
				// Compact JSON can expand when represented by PostgreSQL jsonb. Preserve
				// the fenced outcome as a bounded media failure, never a storage failure.
				completion.Kind = domain.ProbeCompletionFailed
				completion.FailureCode = domain.ProbeFailureMetadataLimit
			} else {
				charge := int64(domain.ProbeRowAllowanceBytes) + metadataBytes
				tag, e2 := tx.Exec(ctx, `UPDATE probe_cache SET state='ready',metadata=$3::jsonb,error_code='',expires_at=clock_timestamp()+$4*interval '1 second',retry_after=NULL,failure_count=0,charge_bytes=$5,lease_owner=NULL,lease_generation=NULL,lease_until=NULL,lease_job_id=NULL,lease_job_generation=NULL,updated_at=clock_timestamp(),last_used_at=clock_timestamp() WHERE root_id=$1::uuid AND relative_path=$2 AND lease_generation=$6 AND lease_until>clock_timestamp()`, e.Inventory.RootID, e.Inventory.Path, string(payloads[i]), int64(policy.PositiveTTL/time.Second), charge, cached.lease.Generation)
				if e2 != nil {
					return domain.ProbePhase{}, storageError(e2)
				}
				if tag.RowsAffected() != 1 {
					return domain.ProbePhase{}, domain.ErrProbeLeaseLost
				}
				if err = adjustProbeQuota(ctx, tx, p.LibraryID, 0, charge-cached.charge, -1); err != nil {
					return domain.ProbePhase{}, err
				}
			}
		}
		switch completion.Kind {
		case domain.ProbeCompletionHit, domain.ProbeCompletionNegativeHit:
			_, err = tx.Exec(ctx, `UPDATE probe_cache SET last_used_at=clock_timestamp() WHERE root_id=$1::uuid AND relative_path=$2 AND last_used_at<=clock_timestamp()-interval '1 hour'`, e.Inventory.RootID, e.Inventory.Path)
			if completion.Kind == domain.ProbeCompletionHit {
				progress.Hits++
			} else {
				progress.NegativeHits++
			}
		case domain.ProbeCompletionSucceeded:
			progress.Succeeded++
		case domain.ProbeCompletionFailed:
			tag, e2 := tx.Exec(ctx, `UPDATE probe_cache SET state='failed',metadata=NULL,error_code=$3,expires_at=clock_timestamp()+$4*interval '1 second',retry_after=clock_timestamp()+$4*interval '1 second',failure_count=LEAST(failure_count+1,10),charge_bytes=2048,lease_owner=NULL,lease_generation=NULL,lease_until=NULL,lease_job_id=NULL,lease_job_generation=NULL,updated_at=clock_timestamp(),last_used_at=clock_timestamp() WHERE root_id=$1::uuid AND relative_path=$2 AND lease_generation=$5 AND lease_until>clock_timestamp()`, e.Inventory.RootID, e.Inventory.Path, string(completion.FailureCode), int64(policy.NegativeTTL/time.Second), cached.lease.Generation)
			if e2 != nil {
				return domain.ProbePhase{}, storageError(e2)
			}
			if tag.RowsAffected() != 1 {
				return domain.ProbePhase{}, domain.ErrProbeLeaseLost
			}
			if err = adjustProbeQuota(ctx, tx, p.LibraryID, 0, int64(domain.ProbeRowAllowanceBytes)-cached.charge, -1); err != nil {
				return domain.ProbePhase{}, err
			}
			progress.Failed++
		case domain.ProbeCompletionChanged, domain.ProbeCompletionUnavailable:
			if completion.Lease != nil {
				err = deleteProbeRow(ctx, tx, cached)
			}
			if completion.Kind == domain.ProbeCompletionChanged {
				progress.Changed++
			} else {
				progress.Unavailable++
			}
		}
		if err != nil {
			return domain.ProbePhase{}, storageError(err)
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE probe_job_state p SET cursor_inventory_id=$2::uuid,revision=revision+1,processed=processed+$3,hits=hits+$4,negative_hits=negative_hits+$5,succeeded=succeeded+$6,failed=failed+$7,changed=changed+$8,unavailable=unavailable+$9,updated_at=clock_timestamp() FROM jobs j WHERE p.job_id=$1::uuid AND j.id=p.job_id AND p.processed+$3<=j.max_entries`, l.Job.ID, entries[len(entries)-1].Inventory.ID, len(entries), progress.Hits, progress.NegativeHits, progress.Succeeded, progress.Failed, progress.Changed, progress.Unavailable)
	if err != nil {
		return domain.ProbePhase{}, storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ProbePhase{}, domain.ErrScanLimit
	}
	if err = checkProbePhaseScope(ctx, tx, p); err != nil {
		return domain.ProbePhase{}, err
	}
	if latestAllowed.IsZero() {
		err = guardProbeParent(ctx, tx, l)
	} else {
		err = guardProbeParentExpiry(ctx, tx, l, latestAllowed)
	}
	if err != nil {
		return domain.ProbePhase{}, err
	}
	result, err := loadProbePhase(ctx, tx, l.Job.ID)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProbePhase{}, storageError(err)
	}
	return result, nil
}
