package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func storeNFOSummary(ctx context.Context, tx pgx.Tx, p domain.NFOPhase, e nfoEntry, c domain.NFOCompletion, raw []byte) error {
	policy, err := readNFOCachePolicy(ctx, tx)
	if err != nil {
		return err
	}
	var bytes int64
	if err = tx.QueryRow(ctx, `SELECT octet_length($1::jsonb::text)`, string(raw)).Scan(&bytes); err != nil {
		return storageError(err)
	}
	if bytes > domain.NFOSummaryMaxBytes {
		return domain.ErrNFOSummaryLimit
	}
	charge := int64(domain.NFORowAllowanceBytes) + bytes
	old, err := readCachedNFO(ctx, tx, e.Inventory.RootID, e.Inventory.Path)
	deltaRows, deltaBytes := int64(1), charge
	if err == nil {
		deltaRows = 0
		deltaBytes -= old.charge
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if err = ensureNFOLibrary(ctx, tx, p.LibraryID); err != nil {
		return err
	}
	if err = makeNFOCapacity(ctx, tx, p.LibraryID, deltaRows, deltaBytes, e.Inventory.RootID, e.Inventory.Path); err != nil {
		return err
	}
	ttl := policy.PositiveTTL
	if c.Summary.Status == domain.NFOStatusInvalid {
		ttl = policy.NegativeTTL
	}
	_, err = tx.Exec(ctx, `INSERT INTO nfo_cache(root_id,relative_path,library_id,size,modified_unix_nano,source_sha256,fingerprint_version,identity_digest,library_generation,root_generation,status,summary,expires_at,charge_bytes) VALUES($1::uuid,$2,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,clock_timestamp()+$13*interval '1 second',$14) ON CONFLICT(root_id,relative_path) DO UPDATE SET observation_id=gen_random_uuid(),size=EXCLUDED.size,modified_unix_nano=EXCLUDED.modified_unix_nano,source_sha256=EXCLUDED.source_sha256,fingerprint_version=EXCLUDED.fingerprint_version,identity_digest=EXCLUDED.identity_digest,library_generation=EXCLUDED.library_generation,root_generation=EXCLUDED.root_generation,status=EXCLUDED.status,summary=EXCLUDED.summary,expires_at=EXCLUDED.expires_at,charge_bytes=EXCLUDED.charge_bytes,last_used_at=clock_timestamp(),updated_at=clock_timestamp()`, e.Inventory.RootID, e.Inventory.Path, p.LibraryID, c.Candidate.Stamp.Size, c.Candidate.Stamp.ModifiedUnixNano, probeBytes(c.Candidate.Stamp.SHA256), c.Candidate.Stamp.FingerprintVersion, probeBytes(p.IdentityDigest), p.LibraryGeneration, e.rootGeneration, c.Summary.Status, string(raw), int64(ttl/time.Second), charge)
	if err != nil {
		return storageError(err)
	}
	return adjustNFOQuota(ctx, tx, p.LibraryID, deltaRows, deltaBytes)
}
func (s *Store) CommitNFOBatch(parent context.Context, l domain.JobLease, token domain.NFOPageToken, batch []domain.NFOCompletion) (domain.NFOPhase, error) {
	if err := domain.ValidateNFOPageToken(token); err != nil {
		return domain.NFOPhase{}, err
	}
	if err := domain.ValidateNFOCompletionBatch(batch); err != nil {
		return domain.NFOPhase{}, err
	}
	candidates := make([]domain.NFOCandidate, len(batch))
	var raw []byte
	for i, c := range batch {
		candidates[i] = c.Candidate
		if c.Kind == domain.NFOCompletionParsed {
			var err error
			raw, err = domain.MarshalNFOSummary(*c.Summary)
			if err != nil {
				return domain.NFOPhase{}, err
			}
		}
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedNFOPhase(ctx, tx, l, true)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	stamp := batch[0].Kind == domain.NFOCompletionHit || batch[0].Kind == domain.NFOCompletionNegativeHit || batch[0].Kind == domain.NFOCompletionParsed
	entries, err := checkNFOPrefix(ctx, tx, p, token, candidates, stamp)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	progress := p.Progress
	var earliest *time.Time
	for i, c := range batch {
		var summary *domain.NFOValidationSummary
		switch c.Kind {
		case domain.NFOCompletionHit, domain.NFOCompletionNegativeHit:
			kind, cached, e := nfoLookup(ctx, tx, p, entries[i], c.Candidate)
			if e != nil {
				return domain.NFOPhase{}, e
			}
			if kind != c.Kind {
				return domain.NFOPhase{}, domain.ErrConflict
			}
			summary = &cached.summary
			if earliest == nil || cached.expires.Before(*earliest) {
				expires := cached.expires
				earliest = &expires
			}
			if c.Kind == domain.NFOCompletionHit {
				progress.Hits++
			} else {
				progress.NegativeHits++
			}
			if _, err = tx.Exec(ctx, `UPDATE nfo_cache SET last_used_at=clock_timestamp() WHERE root_id=$1::uuid AND relative_path=$2`, entries[i].Inventory.RootID, entries[i].Inventory.Path); err != nil {
				return domain.NFOPhase{}, storageError(err)
			}
		case domain.NFOCompletionParsed:
			if err = storeNFOSummary(ctx, tx, p, entries[i], c, raw); err != nil {
				return domain.NFOPhase{}, err
			}
			progress.Parsed++
			summary = c.Summary
		case domain.NFOCompletionChanged:
			progress.Changed++
		case domain.NFOCompletionUnavailable:
			progress.Unavailable++
		case domain.NFOCompletionRejected:
			progress.Rejected++
		}
		if summary != nil {
			if summary.Status == domain.NFOStatusValid {
				progress.Valid++
			} else {
				progress.Invalid++
			}
			if summary.WarningCount > 0 {
				progress.WarningFiles++
			}
		}
		progress.Processed++
	}
	if err = domain.ValidateNFOProgress(progress); err != nil {
		return domain.NFOPhase{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE nfo_job_state SET cursor_inventory_id=$2::uuid,revision=revision+1,processed=$3,hits=$4,negative_hits=$5,parsed=$6,valid=$7,invalid=$8,warning_files=$9,changed=$10,unavailable=$11,rejected=$12,updated_at=clock_timestamp() WHERE job_id=$1::uuid`, l.Job.ID, candidates[len(candidates)-1].InventoryID, progress.Processed, progress.Hits, progress.NegativeHits, progress.Parsed, progress.Valid, progress.Invalid, progress.WarningFiles, progress.Changed, progress.Unavailable, progress.Rejected)
	if err != nil {
		return domain.NFOPhase{}, storageError(err)
	}
	// Locked cache rows preserve every checked key and status. The final parent
	// fence also compares the earliest hit TTL with the DB wall clock, so a wait
	// while writing the checkpoint cannot silently turn an expired row into a hit.
	if err = checkNFOPhaseScope(ctx, tx, p); err != nil {
		return domain.NFOPhase{}, err
	}
	p, err = loadNFOPhase(ctx, tx, l.Job.ID)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	return commitNFOPhaseBefore(ctx, tx, l, p, earliest)
}
