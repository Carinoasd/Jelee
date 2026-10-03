package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// The jobs bit survives a missing request. Neither half may turn an enabled
// job into an unfiltered scan when the retained state is inconsistent.
func loadIgnoreRequest(ctx context.Context, tx pgx.Tx, id string) (*domain.IgnoreRequest, error) {
	return loadExecutionIgnoreRequest(ctx, tx, id, false)
}

func loadExecutionIgnoreRequest(ctx context.Context, tx pgx.Tx, id string, familyAllowed bool) (*domain.IgnoreRequest, error) {
	var requested bool
	var library string
	if err := tx.QueryRow(ctx, `SELECT ignore_requested,library_id::text FROM jobs WHERE id=$1::uuid`, id).Scan(&requested, &library); err != nil {
		return nil, storageError(err)
	}
	var r domain.IgnoreRequest
	err := tx.QueryRow(ctx, `SELECT job_id::text,library_id::text,mode,case_mode,program_version,proof_version FROM job_ignore_requests WHERE job_id=$1::uuid FOR UPDATE`, id).Scan(&r.JobID, &r.LibraryID, &r.Intent.Mode, &r.Intent.CaseMode, &r.Identity.ProgramVersion, &r.Identity.ProofVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		if requested {
			return nil, domain.ErrConflict
		}
		return nil, nil
	}
	if err != nil {
		return nil, storageError(err)
	}
	valid := domain.ValidateIgnoreRequest(r) == nil || familyAllowed && domain.ValidateFamilyIgnoreRequest(r) == nil
	if !requested || r.LibraryID != library || !valid {
		return nil, domain.ErrConflict
	}
	return &r, nil
}

func insertIgnoreRequest(ctx context.Context, tx pgx.Tx, r domain.IgnoreRequest) error {
	return insertAdmissionIgnoreRequest(ctx, tx, r, false)
}
func insertAdmissionIgnoreRequest(ctx context.Context, tx pgx.Tx, r domain.IgnoreRequest, familyAllowed bool) error {
	if domain.ValidateIgnoreRequest(r) != nil && !(familyAllowed && domain.ValidateFamilyIgnoreRequest(r) == nil) {
		return domain.ErrInvalid
	}
	_, err := tx.Exec(ctx, `INSERT INTO job_ignore_requests(job_id,library_id,mode,case_mode,program_version,proof_version) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6)`, r.JobID, r.LibraryID, r.Intent.Mode, r.Intent.CaseMode, r.Identity.ProgramVersion, r.Identity.ProofVersion)
	return storageError(err)
}

// C1 stores intent but has no filtered inventory executor. Keep every direct
// execution path closed as well as claims; a caller-supplied capability or a
// manufactured live lease cannot authorize an unfiltered fallback.
func requireIgnoreOff(ctx context.Context, tx pgx.Tx, id string) error {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT ignore_requested OR EXISTS(SELECT 1 FROM job_ignore_requests r WHERE r.job_id=j.id) FROM jobs j WHERE j.id=$1::uuid`, id).Scan(&enabled); err != nil {
		return storageError(err)
	}
	if enabled {
		return domain.ErrIgnoreUnavailable
	}
	return nil
}
