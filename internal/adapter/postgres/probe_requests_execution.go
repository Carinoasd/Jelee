package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.ProbeExecutionRepository = (*Store)(nil)

func (s *Store) LoadProbeWork(parent context.Context, l domain.JobLease) (domain.ProbeWork, error) {
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.ProbeWork{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.ProbeWork{}, err
	}
	if current.Job.CancelRequested {
		return domain.ProbeWork{}, context.Canceled
	}
	r, err := loadProbeRequest(ctx, tx, l.Job.ID)
	if err != nil {
		return domain.ProbeWork{}, err
	}
	work := domain.ProbeWork{Request: r}
	p, err := loadProbePhase(ctx, tx, l.Job.ID)
	if err == nil {
		if r != nil && !requestMatchesPhase(r, p) {
			return domain.ProbeWork{}, domain.ErrProbeIdentityMismatch
		}
		work.Phase = &p
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.ProbeWork{}, err
	}
	if err = guardProbeParent(ctx, tx, l); err != nil {
		return domain.ProbeWork{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProbeWork{}, storageError(err)
	}
	return work, nil
}

func (s *Store) BeginRequestedProbePhase(parent context.Context, l domain.JobLease) (domain.ProbePhase, error) {
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	if _, err = readProbePolicy(ctx, tx); err != nil {
		return domain.ProbePhase{}, err
	}
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	if current.Job.CancelRequested {
		return domain.ProbePhase{}, context.Canceled
	}
	r, err := loadProbeRequest(ctx, tx, l.Job.ID)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	if r == nil {
		return domain.ProbePhase{}, domain.ErrNotFound
	}
	if r.ErrorCode != "" {
		return domain.ProbePhase{}, domain.ErrConflict
	}
	requested := domain.ProbePhase{JobID: r.JobID, LibraryID: r.LibraryID, Start: requestPhaseStart(r), LibraryGeneration: r.LibraryGeneration, TargetItemGeneration: r.TargetItemGeneration}
	if err = checkProbePhaseScope(ctx, tx, requested); err != nil {
		return domain.ProbePhase{}, err
	}
	p, err := loadProbePhase(ctx, tx, l.Job.ID)
	if err == nil {
		if !requestMatchesPhase(r, p) {
			return domain.ProbePhase{}, domain.ErrProbeIdentityMismatch
		}
		if p.State == domain.ProbePhaseAborted {
			return domain.ProbePhase{}, domain.ErrConflict
		}
	} else if errors.Is(err, domain.ErrNotFound) {
		var pending bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_directories WHERE job_id=$1::uuid AND NOT done)`, l.Job.ID).Scan(&pending); err != nil {
			return domain.ProbePhase{}, storageError(err)
		}
		if pending {
			return domain.ProbePhase{}, domain.ErrConflict
		}
		// Enqueue already invalidated the requested scope. Recovery must use
		// exactly those generations and must never invalidate a second time.
		if _, err = tx.Exec(ctx, `INSERT INTO probe_job_state(job_id,library_id,tool_version_id,phase,scope,library_generation,target_item_id,target_item_generation) VALUES($1::uuid,$2::uuid,$3::uuid,'running',$4,$5,NULLIF($6,'')::uuid,NULLIF($7,0))`, r.JobID, r.LibraryID, r.Identity.ID, r.Intent.Scope, r.LibraryGeneration, r.Intent.TargetItemID, r.TargetItemGeneration); err != nil {
			return domain.ProbePhase{}, storageError(err)
		}
		p, err = loadProbePhase(ctx, tx, l.Job.ID)
		if err != nil {
			return domain.ProbePhase{}, err
		}
	} else {
		return domain.ProbePhase{}, err
	}
	if err = guardProbeParent(ctx, tx, l); err != nil {
		return domain.ProbePhase{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProbePhase{}, storageError(err)
	}
	return p, nil
}

func (s *Store) AbortProbeRequest(parent context.Context, l domain.JobLease, code domain.ProbePhaseError) error {
	if !domain.ValidProbePhaseError(code) {
		return domain.ErrInvalid
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return err
	}
	if current.Job.CancelRequested {
		return context.Canceled
	}
	r, err := loadProbeRequest(ctx, tx, l.Job.ID)
	if err != nil {
		return err
	}
	if r == nil {
		return domain.ErrNotFound
	}
	if r.ErrorCode != "" && r.ErrorCode != code {
		return domain.ErrConflict
	}
	p, phaseErr := loadProbePhase(ctx, tx, l.Job.ID)
	if phaseErr == nil {
		if p.State == domain.ProbePhaseDone {
			return domain.ErrConflict
		}
		if p.State == domain.ProbePhaseAborted && p.ErrorCode != code {
			return domain.ErrConflict
		}
	} else if !errors.Is(phaseErr, domain.ErrNotFound) {
		return phaseErr
	}
	if err = releaseParentProbeLeases(ctx, tx, l.Job.ID); err != nil {
		return err
	}
	if r.ErrorCode == "" {
		if _, err = tx.Exec(ctx, `UPDATE probe_requests SET error_code=$2 WHERE job_id=$1::uuid`, l.Job.ID, string(code)); err != nil {
			return storageError(err)
		}
	}
	if phaseErr == nil && p.State != domain.ProbePhaseAborted {
		if _, err = tx.Exec(ctx, `UPDATE probe_job_state SET phase='aborted',error_code=$2,revision=revision+1,updated_at=clock_timestamp() WHERE job_id=$1::uuid`, l.Job.ID, string(code)); err != nil {
			return storageError(err)
		}
	}
	if err = guardProbeParent(ctx, tx, l); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
