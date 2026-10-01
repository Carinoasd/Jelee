package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.ScanJobRepository = (*Store)(nil)
var _ app.NFOExecutionRepository = (*Store)(nil)

func (s *Store) SubmitScanWithStages(ctx context.Context, a domain.Actor, library, key, priority string, intent domain.ScanIntent, p domain.JobPolicy, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity) (domain.Job, bool, error) {
	return s.submitScanJob(ctx, a, library, "", key, priority, intent, p, probe, nfo)
}
func (s *Store) RetryScanWithStages(ctx context.Context, a domain.Actor, parent, key string, p domain.JobPolicy, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity) (domain.Job, bool, error) {
	if !domain.ValidID(parent) {
		return domain.Job{}, false, domain.ErrNotFound
	}
	return s.submitScanJob(ctx, a, "", parent, key, "", domain.ScanIntent{}, p, probe, nfo)
}
func loadNFORequest(ctx context.Context, tx pgx.Tx, id string) (*domain.NFORequest, error) {
	var r domain.NFORequest
	err := tx.QueryRow(ctx, `SELECT job_id::text,library_id::text,requested,mode,parser_version,summary_schema_version,fingerprint_version,max_source_bytes,encode(identity_digest,'hex'),library_generation,error_code FROM nfo_job_requests WHERE job_id=$1::uuid FOR UPDATE`, id).Scan(&r.JobID, &r.LibraryID, &r.Requested, &r.Mode, &r.Identity.ParserVersion, &r.Identity.SummarySchemaVersion, &r.Identity.FingerprintVersion, &r.Identity.MaxSourceBytes, &r.IdentityDigest, &r.LibraryGeneration, &r.ErrorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError(err)
	}
	return &r, nil
}
func nfoRequestMatchesPhase(r *domain.NFORequest, p domain.NFOPhase) bool {
	return r.JobID == p.JobID && r.LibraryID == p.LibraryID && r.Mode == p.Mode && r.Identity == p.Identity && r.IdentityDigest == p.IdentityDigest && r.LibraryGeneration == p.LibraryGeneration
}
func prepareNFORequest(ctx context.Context, tx pgx.Tx, library string, requested bool, identity *domain.NFOIdentity) (domain.NFORequest, error) {
	policy, err := readNFOLibraryPolicy(ctx, tx, library)
	if err != nil {
		return domain.NFORequest{}, err
	}
	r := domain.NFORequest{LibraryID: library, Requested: requested, Mode: domain.NFOModeOff, Identity: domain.DefaultNFOIdentity(), LibraryGeneration: policy.Generation}
	if requested {
		if policy.Mode != domain.NFOModeReadOnly {
			return domain.NFORequest{}, domain.ErrNFODisabled
		}
		if identity == nil {
			return domain.NFORequest{}, domain.ErrNFOReaderUnavailable
		}
		if err = domain.ValidateNFOIdentity(*identity); err != nil {
			return domain.NFORequest{}, err
		}
		if _, err = readNFOCachePolicy(ctx, tx); err != nil {
			return domain.NFORequest{}, err
		}
		if err = ensureNFOLibrary(ctx, tx, library); err != nil {
			return domain.NFORequest{}, err
		}
		r.Mode, r.Identity = domain.NFOModeReadOnly, *identity
	}
	r.IdentityDigest, err = domain.NFOIdentityDigest(r.Identity)
	if err != nil {
		return domain.NFORequest{}, err
	}
	return r, nil
}
func insertNFORequest(ctx context.Context, tx pgx.Tx, r domain.NFORequest) error {
	_, err := tx.Exec(ctx, `INSERT INTO nfo_job_requests(job_id,library_id,requested,mode,parser_version,summary_schema_version,fingerprint_version,max_source_bytes,identity_digest,library_generation) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10)`, r.JobID, r.LibraryID, r.Requested, r.Mode, r.Identity.ParserVersion, r.Identity.SummarySchemaVersion, r.Identity.FingerprintVersion, r.Identity.MaxSourceBytes, probeBytes(r.IdentityDigest), r.LibraryGeneration)
	if err != nil {
		return storageError(err)
	}
	phase, code := domain.NFOPhaseWaiting, ""
	if !r.Requested {
		phase, code = domain.NFOPhaseAborted, string(domain.NFOPhaseDisabled)
	}
	_, err = tx.Exec(ctx, `INSERT INTO nfo_job_state(job_id,library_id,mode,phase,parser_version,summary_schema_version,fingerprint_version,max_source_bytes,identity_digest,library_generation,error_code) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, r.JobID, r.LibraryID, r.Mode, phase, r.Identity.ParserVersion, r.Identity.SummarySchemaVersion, r.Identity.FingerprintVersion, r.Identity.MaxSourceBytes, probeBytes(r.IdentityDigest), r.LibraryGeneration, code)
	return storageError(err)
}
func (s *Store) LoadNFOWork(parent context.Context, l domain.JobLease) (domain.NFOWork, error) {
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.NFOWork{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.NFOWork{}, err
	}
	if current.Job.CancelRequested {
		return domain.NFOWork{}, context.Canceled
	}
	if err = requireIgnoreOff(ctx, tx, l.Job.ID); err != nil {
		return domain.NFOWork{}, err
	}
	r, err := loadNFORequest(ctx, tx, l.Job.ID)
	if err != nil {
		return domain.NFOWork{}, err
	}
	work := domain.NFOWork{Request: r}
	p, err := loadNFOPhase(ctx, tx, l.Job.ID)
	if err == nil {
		if r != nil && !nfoRequestMatchesPhase(r, p) {
			return domain.NFOWork{}, domain.ErrNFOIdentityMismatch
		}
		if r == nil && p.Mode == domain.NFOModeReadOnly {
			return domain.NFOWork{}, domain.ErrConflict
		}
		work.Phase = &p
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.NFOWork{}, err
	} else if r != nil {
		return domain.NFOWork{}, domain.ErrConflict
	}
	if _, err = commitNFOPhase(ctx, tx, l, domain.NFOPhase{}); err != nil {
		return domain.NFOWork{}, err
	}
	return work, nil
}
func (s *Store) BeginRequestedNFOPhase(parent context.Context, l domain.JobLease) (domain.NFOPhase, error) {
	return s.BeginNFOPhase(parent, l)
}
func (s *Store) AbortNFORequest(parent context.Context, l domain.JobLease, code domain.NFOPhaseError) error {
	if !domain.ValidNFOPhaseError(code) {
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
	r, err := loadNFORequest(ctx, tx, l.Job.ID)
	if err != nil {
		return err
	}
	if r == nil || !r.Requested {
		return domain.ErrNotFound
	}
	if r.ErrorCode != "" && r.ErrorCode != code {
		return domain.ErrConflict
	}
	p, err := loadNFOPhase(ctx, tx, l.Job.ID)
	if err != nil {
		return err
	}
	if !nfoRequestMatchesPhase(r, p) {
		return domain.ErrNFOIdentityMismatch
	}
	if p.State == domain.NFOPhaseDone || p.State == domain.NFOPhaseAborted && p.ErrorCode != code {
		return domain.ErrConflict
	}
	if r.ErrorCode == "" {
		if _, err = tx.Exec(ctx, `UPDATE nfo_job_requests SET error_code=$2 WHERE job_id=$1::uuid`, l.Job.ID, string(code)); err != nil {
			return storageError(err)
		}
	}
	if p.State != domain.NFOPhaseAborted {
		if _, err = tx.Exec(ctx, `UPDATE nfo_job_state SET phase='aborted',error_code=$2,revision=revision+1,updated_at=clock_timestamp() WHERE job_id=$1::uuid`, l.Job.ID, string(code)); err != nil {
			return storageError(err)
		}
	}
	_, err = commitNFOPhase(ctx, tx, l, p)
	return err
}
