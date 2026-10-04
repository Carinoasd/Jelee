package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func guardIgnoreSeal(ctx context.Context, tx pgx.Tx, l domain.JobLease) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT v.generation=$2 AND v.completed AND v.deadline>clock_timestamp() AND COALESCE(v.sealed_until>clock_timestamp(),false) AND m.frozen AND NOT m.invalidated AND v.verified_rows=m.rows FROM job_ignore_verifications v JOIN job_ignore_manifests m ON m.job_id=v.job_id WHERE v.job_id=$1::uuid`, l.Job.ID, l.Generation).Scan(&valid)
	if err != nil {
		return storageError(err)
	}
	if !valid {
		return domain.ErrInventoryInvalidated
	}
	return nil
}

func ignoreDecisionTable(family bool) string {
	if family {
		return "job_ignore_family_decisions"
	}
	return "job_ignore_decisions"
}

func guardIgnorePublicationSeal(ctx context.Context, tx pgx.Tx, l domain.JobLease, family bool) error {
	if family {
		if err := guardFamilyVerificationComplete(ctx, tx, l); err != nil {
			return err
		}
	}
	return guardIgnoreSeal(ctx, tx, l)
}

func saveIgnoreImageProgress(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64, comparable, family bool) error {
	var p domain.ImageProgress
	if comparable {
		if err := tx.QueryRow(ctx, imageCurrentCountsSQL, l.Job.LibraryID, l.Job.ID, epoch).Scan(&p.Added, &p.Changed, &p.Unchanged, &p.Uncompared); err != nil {
			return storageError(err)
		}
		err := tx.QueryRow(ctx, ignoreImageMissingSQL(family), l.Job.LibraryID, l.Job.ID).Scan(&p.Missing)
		if err != nil {
			return storageError(err)
		}
		p.ComparisonComplete = p.Uncompared == 0
	} else {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM job_inventory WHERE job_id=$1::uuid AND kind='image'`, l.Job.ID).Scan(&p.Uncompared); err != nil {
			return storageError(err)
		}
	}
	if err := domain.ValidateImageProgress(p); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO image_job_state(job_id,library_id,added,changed,unchanged,missing,uncompared,comparison_complete) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8) ON CONFLICT(job_id) DO UPDATE SET added=EXCLUDED.added,changed=EXCLUDED.changed,unchanged=EXCLUDED.unchanged,missing=EXCLUDED.missing,uncompared=EXCLUDED.uncompared,comparison_complete=EXCLUDED.comparison_complete`, l.Job.ID, l.Job.LibraryID, p.Added, p.Changed, p.Unchanged, p.Missing, p.Uncompared, p.ComparisonComplete)
	return storageError(err)
}

// FinishIgnoreJob publishes only an explicitly classified, freshly sealed
// inventory. Ordinary FinishJob remains guarded, as do enabled job claims.
// Failure/cancellation continues to use FinishJob without replacing baseline.
func (s *Store) FinishIgnoreJob(ctx context.Context, l domain.JobLease) error {
	return s.finishIgnoreJob(ctx, l, false)
}

func (s *Store) FinishFamilyIgnoreJob(ctx context.Context, l domain.JobLease) error {
	return s.finishIgnoreJob(ctx, l, true)
}

func (s *Store) finishIgnoreJob(ctx context.Context, l domain.JobLease, family bool) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, revision, err := comparisonModeFence(ctx, tx, l, family)
	if err != nil {
		return err
	}
	c, err := loadIgnoreComparison(ctx, tx, l.Job.ID)
	if err != nil {
		return err
	}
	if c.epoch != epoch || c.token.BaselineRevision != revision {
		return domain.ErrInventoryInvalidated
	}
	if !c.complete {
		return domain.ErrConflict
	}
	// Unknown evidence can terminate as review-required, but cannot publish
	// a baseline or report confirmed missing files without verification.
	sealed := c.counts.Unknown == 0
	if sealed {
		if err = guardIgnorePublicationSeal(ctx, tx, current, family); err != nil {
			return err
		}
	}
	if err = requireNFOPhaseFinished(ctx, tx, l.Job.ID); err != nil {
		return err
	}
	phase, phaseErr := loadProbePhase(ctx, tx, l.Job.ID)
	request, err := loadProbeRequest(ctx, tx, l.Job.ID)
	if err != nil {
		return err
	}
	if request != nil {
		if request.ErrorCode != "" || errors.Is(phaseErr, domain.ErrNotFound) {
			return domain.ErrConflict
		}
		if phaseErr == nil && !requestMatchesPhase(request, phase) {
			return domain.ErrProbeIdentityMismatch
		}
	}
	if phaseErr == nil {
		if phase.State != domain.ProbePhaseDone {
			return domain.ErrConflict
		}
		if err = checkProbePhaseScope(ctx, tx, phase); err != nil {
			return err
		}
	} else if !errors.Is(phaseErr, domain.ErrNotFound) {
		return phaseErr
	}
	covered, skipped, err := inventoryCoverage(ctx, tx, current)
	if err != nil {
		return err
	}
	if !covered || skipped || current.Job.Skipped != 0 {
		return domain.ErrConflict
	}
	result, err := domain.CompareIgnoreBaseline(c.counts, true, c.comparable, current.Policy.MissingCountLimit, current.Policy.MissingPercentLimit)
	if err != nil {
		return err
	}
	if err = saveIgnoreImageProgress(ctx, tx, current, epoch, c.comparable && sealed, family); err != nil {
		return err
	}
	if !result.ReviewRequired {
		var count int64
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM job_inventory WHERE job_id=$1::uuid`, l.Job.ID).Scan(&count); err != nil {
			return storageError(err)
		}
		if c.comparable {
			count += c.counts.Excluded
		}
		if count > 500000 || count > int64(current.Policy.MaxEntries) {
			return domain.ErrScanLimit
		}
		mode := domain.IgnoreModeJeleeignore
		if family {
			mode = domain.IgnoreModeFamily
		}
		excluded := int64(0)
		if c.comparable {
			excluded = c.counts.Excluded
		}
		published, publishErr := publishPreparedInventory(ctx, tx, current, mode, excluded)
		if publishErr != nil {
			return publishErr
		}
		if !published {
			// Keep only classified excluded rows from this same scope, untouched:
			// attributes, source epoch and observed revision remain historical.
			_, err = tx.Exec(ctx, `DELETE FROM library_inventory_baseline b WHERE b.library_id=$1::uuid AND (NOT $3::boolean OR NOT EXISTS(SELECT 1 FROM `+ignoreDecisionTable(family)+` d WHERE d.job_id=$2::uuid AND d.root_id=b.root_id AND d.path=b.path AND d.outcome='excluded'))`, current.Job.LibraryID, l.Job.ID, c.comparable)
			if err != nil {
				return storageError(err)
			}
			_, err = tx.Exec(ctx, `WITH revision AS (UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid RETURNING inventory_baseline_revision) INSERT INTO library_inventory_baseline(library_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision) SELECT $1::uuid,root_id,path,true,kind,size,modified_unix_nano,$3,revision.inventory_baseline_revision FROM job_inventory CROSS JOIN revision WHERE job_id=$2::uuid`, current.Job.LibraryID, l.Job.ID, epoch)
			if err != nil {
				return storageError(err)
			}
		}
	}
	if err = releaseParentProbeLeases(ctx, tx, l.Job.ID); err != nil {
		return err
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET state='succeeded',error_code='',missing=$2,review_required=$3,finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE id=$1::uuid`, l.Job.ID, result.Missing, result.ReviewRequired); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "job.finished", l.Job.ID, nil, map[string]any{"state": domain.JobSucceeded, "errorCode": "", "missing": result.Missing, "reviewRequired": result.ReviewRequired}); err != nil {
		return err
	}
	if err = appendScanFinished(ctx, tx, s.webhooksOn(), current.Job, domain.JobSucceeded, "", result.Missing, result.ReviewRequired); err != nil {
		return err
	}
	if err = trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return err
	}
	if err = guardInventoryFinish(ctx, tx, current, &epoch, true); err != nil {
		return err
	}
	if sealed {
		if err = guardIgnorePublicationSeal(ctx, tx, current, family); err != nil {
			return err
		}
	}
	return storageError(tx.Commit(ctx))
}

func ignoreImageMissingSQL(family bool) string {
	return `WITH baseline AS MATERIALIZED (
 SELECT root_id,path FROM library_inventory_baseline_data
 WHERE library_id=$1::uuid AND snapshot_id=(SELECT active_inventory_snapshot FROM libraries WHERE id=$1::uuid) AND kind='image'),
 current AS MATERIALIZED (SELECT root_id,path,kind FROM job_inventory WHERE job_id=$2::uuid),
 missing AS (
 SELECT root_id,path FROM current WHERE kind<>'image'
 UNION
 (SELECT root_id,path FROM ` + ignoreDecisionTable(family) + ` WHERE job_id=$2::uuid AND outcome='included_missing'
 EXCEPT SELECT root_id,path FROM current)),
 lost AS (SELECT root_id,path FROM baseline INTERSECT SELECT root_id,path FROM missing)
 SELECT count(*) FROM lost`
}
