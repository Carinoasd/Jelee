package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
	"github.com/jackc/pgx/v5"
)

var _ app.ProbeJobRepository = (*Store)(nil)

const probeRequestColumns = `r.job_id::text,r.library_id::text,r.scope,COALESCE(r.target_item_id::text,''),r.tool_version_id::text,encode(t.identity_digest,'hex'),r.library_generation,COALESCE(r.target_item_generation,0),r.error_code`

func loadProbeRequest(ctx context.Context, tx pgx.Tx, id string) (*domain.ProbeRequest, error) {
	var r domain.ProbeRequest
	err := tx.QueryRow(ctx, `SELECT `+probeRequestColumns+` FROM probe_requests r JOIN tool_versions t ON t.id=r.tool_version_id WHERE r.job_id=$1::uuid FOR UPDATE OF r`, id).Scan(&r.JobID, &r.LibraryID, &r.Intent.Scope, &r.Intent.TargetItemID, &r.Identity.ID, &r.Identity.Digest, &r.LibraryGeneration, &r.TargetItemGeneration, &r.ErrorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError(err)
	}
	return &r, nil
}
func requestPhaseStart(r *domain.ProbeRequest) domain.ProbePhaseStart {
	return domain.ProbePhaseStart{Identity: r.Identity, Scope: r.Intent.Scope, TargetItemID: r.Intent.TargetItemID}
}
func requestMatchesPhase(r *domain.ProbeRequest, p domain.ProbePhase) bool {
	return r.JobID == p.JobID && r.LibraryID == p.LibraryID && requestPhaseStart(r) == p.Start && r.LibraryGeneration == p.LibraryGeneration && r.TargetItemGeneration == p.TargetItemGeneration
}
func probeAdminStillLive(ctx context.Context, tx pgx.Tx, a domain.Actor) error {
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.id=$1::uuid AND s.id=$2::uuid AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp())`, a.UserID, a.SessionID).Scan(&live); err != nil {
		return storageError(err)
	}
	if !live {
		return domain.ErrUnauthenticated
	}
	return nil
}
func (s *Store) SubmitScanJob(ctx context.Context, a domain.Actor, libraryID, key, priority string, intent domain.ProbeIntent, p domain.JobPolicy, identity *domain.ProbeIdentity) (domain.Job, bool, error) {
	return s.submitScanJob(ctx, a, libraryID, "", key, priority, domain.ScanIntent{Probe: intent}, p, identity, nil)
}
func (s *Store) RetryScanJob(ctx context.Context, a domain.Actor, id, key string, p domain.JobPolicy, identity *domain.ProbeIdentity) (domain.Job, bool, error) {
	if !domain.ValidID(id) {
		return domain.Job{}, false, domain.ErrNotFound
	}
	return s.submitScanJob(ctx, a, "", id, key, "", domain.ScanIntent{}, p, identity, nil)
}
func (s *Store) submitScanJob(parentContext context.Context, a domain.Actor, libraryID, parent, key, priority string, scanIntent domain.ScanIntent, p domain.JobPolicy, identity *domain.ProbeIdentity, nfoIdentity *domain.NFOIdentity) (domain.Job, bool, error) {
	return s.submitScanJobWithIgnore(parentContext, a, libraryID, parent, key, priority, scanIntent, p, identity, nfoIdentity, true)
}
func (s *Store) submitScanJobWithIgnore(parentContext context.Context, a domain.Actor, libraryID, parent, key, priority string, scanIntent domain.ScanIntent, p domain.JobPolicy, identity *domain.ProbeIdentity, nfoIdentity *domain.NFOIdentity, ignoreAvailable bool) (domain.Job, bool, error) {
	return s.submitScanJobWithIgnoreFamilies(parentContext, a, libraryID, parent, key, priority, scanIntent, p, identity, nfoIdentity, app.IgnoreAdmissionCapabilities{Custom: ignoreAvailable}, false)
}
func (s *Store) submitScanJobWithIgnoreFamilies(parentContext context.Context, a domain.Actor, libraryID, parent, key, priority string, scanIntent domain.ScanIntent, p domain.JobPolicy, identity *domain.ProbeIdentity, nfoIdentity *domain.NFOIdentity, capabilities app.IgnoreAdmissionCapabilities, familyAllowed bool) (domain.Job, bool, error) {
	validate := domain.ValidateScanIntent
	if familyAllowed {
		validate = domain.ValidateScanIntentWithFamilyIgnore
	}
	if parentContext == nil || !validJobPolicy(p) || !validJobKey(key) || validate(scanIntent) != nil {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if parent == "" && ((priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) || (libraryID == "" && scanIntent.Probe.Scope != domain.ProbeScopeItemRebuild) || (libraryID != "" && !domain.ValidID(libraryID))) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parentContext, probeDBTimeout)
	defer cancel()
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.Job{}, false, err
	}
	defer tx.Rollback(ctx)
	job, replay, err := s.submitScanInTransaction(ctx, tx, a, libraryID, parent, key, priority, scanIntent, p, identity, nfoIdentity, capabilities, familyAllowed, func() error { return probeAdminStillLive(ctx, tx, a) })
	if err != nil {
		return domain.Job{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	return job, replay, nil
}

// Caller owns the transaction and supplies its final authority check. Public
// requests use a live session; scheduling uses the persisted enabled owner.
func (s *Store) submitScanInTransaction(ctx context.Context, tx pgx.Tx, a domain.Actor, libraryID, parent, key, priority string, scanIntent domain.ScanIntent, p domain.JobPolicy, identity *domain.ProbeIdentity, nfoIdentity *domain.NFOIdentity, capabilities app.IgnoreAdmissionCapabilities, familyAllowed bool, guard func() error) (domain.Job, bool, error) {
	validateIntent := domain.ValidateScanIntent
	if familyAllowed {
		validateIntent = domain.ValidateScanIntentWithFamilyIgnore
	}
	if !validJobPolicy(p) || !validJobKey(key) || validateIntent(scanIntent) != nil {
		return domain.Job{}, false, domain.ErrInvalid
	}
	intent, nfoRequested, ignoreIntent := scanIntent.Probe, scanIntent.NFO, scanIntent.Ignore
	ignoreIdentity := domain.DefaultIgnoreIdentity()
	if ignoreIntent.Mode == domain.IgnoreModeFamily {
		ignoreIdentity = domain.DefaultFamilyIgnoreIdentity()
	}
	if parent == "" && ((priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) || (libraryID == "" && intent.Scope != domain.ProbeScopeItemRebuild) || (libraryID != "" && !domain.ValidID(libraryID))) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	var err error
	if err = trimJobs(ctx, tx, p.HistoryLimit); err != nil {
		return domain.Job{}, false, err
	}
	if parent == "" && intent.Scope == domain.ProbeScopeItemRebuild {
		var resolved string
		if err = tx.QueryRow(ctx, `SELECT library_id::text FROM items WHERE id=$1::uuid AND ($2='' OR library_id=NULLIF($2,'')::uuid)`, intent.TargetItemID, libraryID).Scan(&resolved); err != nil {
			return domain.Job{}, false, storageError(err)
		}
		libraryID = resolved
	}
	old, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE actor_id=$1::uuid AND idempotency_key=$2`, a.UserID, key))
	if err == nil {
		if old.Kind != "inventory_scan" {
			return domain.Job{}, false, domain.ErrConflict
		}
		var originalParent string
		if err = tx.QueryRow(ctx, `SELECT COALESCE(parent_id::text,'') FROM jobs WHERE id=$1::uuid`, old.ID).Scan(&originalParent); err != nil {
			return domain.Job{}, false, storageError(err)
		}
		request, e := loadProbeRequest(ctx, tx, old.ID)
		if e != nil {
			return domain.Job{}, false, e
		}
		previousIntent := domain.ProbeIntent{}
		if request != nil {
			previousIntent = request.Intent
		}
		oldNFO, e := loadNFORequest(ctx, tx, old.ID)
		if e != nil {
			return domain.Job{}, false, e
		}
		if oldNFO == nil {
			phase, phaseErr := loadNFOPhase(ctx, tx, old.ID)
			if phaseErr != nil && !errors.Is(phaseErr, domain.ErrNotFound) {
				return domain.Job{}, false, phaseErr
			}
			if phaseErr == nil && phase.Mode == domain.NFOModeReadOnly {
				return domain.Job{}, false, domain.ErrConflict
			}
		}
		previousNFO := oldNFO != nil && oldNFO.Requested
		oldIgnore, e := loadExecutionIgnoreRequest(ctx, tx, old.ID, familyAllowed)
		if e != nil {
			return domain.Job{}, false, e
		}
		previousIgnore := domain.IgnoreIntent{}
		if oldIgnore != nil {
			previousIgnore = oldIgnore.Intent
		}
		if originalParent != parent || parent == "" && (old.LibraryID != libraryID || old.Priority != priority || previousIntent != intent || previousNFO != nfoRequested || previousIgnore != ignoreIntent) {
			return domain.Job{}, false, domain.ErrConflict
		}
		if err = guard(); err != nil {
			return domain.Job{}, false, err
		}
		return old, true, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Job{}, false, err
	}
	if parent != "" {
		old, err = scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=$1::uuid`, parent))
		if err != nil {
			return domain.Job{}, false, err
		}
		if old.Kind != "inventory_scan" {
			return domain.Job{}, false, domain.ErrInvalid
		}
		if old.State != domain.JobFailed && old.State != domain.JobCancelled {
			return domain.Job{}, false, domain.ErrConflict
		}
		libraryID, priority = old.LibraryID, old.Priority
		request, e := loadProbeRequest(ctx, tx, parent)
		if e != nil {
			return domain.Job{}, false, e
		}
		if request != nil {
			intent = request.Intent
		}
		oldNFO, e := loadNFORequest(ctx, tx, parent)
		if e != nil {
			return domain.Job{}, false, e
		}
		if oldNFO != nil {
			nfoRequested = oldNFO.Requested
		} else {
			oldPhase, e := loadNFOPhase(ctx, tx, parent)
			if e != nil && !errors.Is(e, domain.ErrNotFound) {
				return domain.Job{}, false, e
			}
			if e == nil && oldPhase.Mode == domain.NFOModeReadOnly {
				return domain.Job{}, false, domain.ErrConflict
			}
		}
		oldIgnore, e := loadExecutionIgnoreRequest(ctx, tx, parent, familyAllowed)
		if e != nil {
			return domain.Job{}, false, e
		}
		if oldIgnore != nil {
			ignoreIntent, ignoreIdentity = oldIgnore.Intent, oldIgnore.Identity
		}
		if validateIntent(domain.ScanIntent{Probe: intent, NFO: nfoRequested, Ignore: ignoreIntent}) != nil {
			return domain.Job{}, false, domain.ErrConflict
		}
	}
	ignoreAvailable := capabilities.Custom
	if ignoreIntent.Mode == domain.IgnoreModeFamily {
		ignoreAvailable = capabilities.Family
	}
	if ignoreIntent.Mode != "" && !ignoreAvailable {
		return domain.Job{}, false, domain.ErrIgnoreUnavailable
	}
	if intent.Scope != "" {
		if identity == nil {
			return domain.Job{}, false, domain.ErrProbeDisabled
		}
		if err = domain.ValidateProbeIdentity(*identity); err != nil {
			return domain.Job{}, false, err
		}
		if _, err = readProbePolicy(ctx, tx); err != nil {
			return domain.Job{}, false, err
		}
	}
	var exists, busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM libraries WHERE id=$1::uuid),EXISTS(SELECT 1 FROM jobs WHERE library_id=$1::uuid AND state IN ('queued','running'))`, libraryID).Scan(&exists, &busy); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if !exists {
		return domain.Job{}, false, domain.ErrNotFound
	}
	var inventoryGeneration int64
	if err = tx.QueryRow(ctx, `SELECT inventory_generation FROM libraries WHERE id=$1::uuid FOR UPDATE`, libraryID).Scan(&inventoryGeneration); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if busy {
		return domain.Job{}, false, domain.ErrJobBusy
	}
	var active, roots int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE state IN ('queued','running')`).Scan(&active); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if active >= p.QueueLimit {
		return domain.Job{}, false, domain.ErrJobQueueFull
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM library_roots WHERE library_id=$1::uuid`, libraryID).Scan(&roots); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if roots == 0 {
		return domain.Job{}, false, domain.ErrScanUnavailable
	}
	if roots > p.MaxDirectories {
		return domain.Job{}, false, domain.ErrScanLimit
	}
	nfoRequest, err := prepareNFORequest(ctx, tx, libraryID, nfoRequested, nfoIdentity)
	if err != nil {
		return domain.Job{}, false, err
	}
	var request *domain.ProbeRequest
	if intent.Scope != "" {
		ref, e := registerProbeIdentity(ctx, tx, *identity)
		if e != nil {
			return domain.Job{}, false, e
		}
		if err = ensureProbeLibrary(ctx, tx, libraryID); err != nil {
			return domain.Job{}, false, err
		}
		request = &domain.ProbeRequest{LibraryID: libraryID, Intent: intent, Identity: ref}
		if intent.Scope == domain.ProbeScopeLibraryRebuild {
			err = tx.QueryRow(ctx, `UPDATE libraries SET probe_generation=probe_generation+1 WHERE id=$1::uuid RETURNING probe_generation`, libraryID).Scan(&request.LibraryGeneration)
		} else {
			err = tx.QueryRow(ctx, `SELECT probe_generation FROM libraries WHERE id=$1::uuid FOR UPDATE`, libraryID).Scan(&request.LibraryGeneration)
		}
		if err != nil {
			return domain.Job{}, false, storageError(err)
		}
		if intent.Scope == domain.ProbeScopeItemRebuild {
			if err = tx.QueryRow(ctx, `UPDATE items SET probe_generation=probe_generation+1 WHERE id=$1::uuid AND library_id=$2::uuid RETURNING probe_generation`, intent.TargetItemID, libraryID).Scan(&request.TargetItemGeneration); err != nil {
				return domain.Job{}, false, storageError(err)
			}
		}
	}
	j, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,parent_id,priority,directory_total,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,inventory_generation,ignore_requested) VALUES($1::uuid,$2::uuid,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING `+jobColumns, libraryID, a.UserID, key, parent, priority, roots, p.QueueLimit, p.HistoryLimit, p.MaxEntries, p.MaxDirectories, p.MaxAttempts, p.MissingCountLimit, p.MissingPercentLimit, inventoryGeneration, ignoreIntent.Mode != ""))
	if err != nil {
		return domain.Job{}, false, err
	}
	tracing.Remember(ctx, tracing.LinkJob, j.ID)
	if _, err = tx.Exec(ctx, `INSERT INTO job_directories(job_id,root_id,path) SELECT $1::uuid,id,'.' FROM library_roots WHERE library_id=$2::uuid`, j.ID, libraryID); err != nil {
		return domain.Job{}, false, storageError(err)
	}
	if request != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO probe_requests(job_id,library_id,scope,target_item_id,tool_version_id,library_generation,target_item_generation) VALUES($1::uuid,$2::uuid,$3,NULLIF($4,'')::uuid,$5::uuid,$6,NULLIF($7,0))`, j.ID, libraryID, intent.Scope, intent.TargetItemID, request.Identity.ID, request.LibraryGeneration, request.TargetItemGeneration); err != nil {
			return domain.Job{}, false, storageError(err)
		}
	}
	nfoRequest.JobID = j.ID
	if err = insertNFORequest(ctx, tx, nfoRequest); err != nil {
		return domain.Job{}, false, err
	}
	if ignoreIntent.Mode != "" {
		if err = insertAdmissionIgnoreRequest(ctx, tx, domain.IgnoreRequest{JobID: j.ID, LibraryID: libraryID, Intent: ignoreIntent, Identity: ignoreIdentity}, familyAllowed); err != nil {
			return domain.Job{}, false, err
		}
	}
	if err = auditAccount(ctx, tx, a, "job.submitted", j.ID, nil, map[string]any{"job": j, "probeScope": intent.Scope, "probeTargetItemId": intent.TargetItemID, "nfo": nfoRequested, "ignoreMode": ignoreIntent.Mode, "ignoreCaseMode": ignoreIntent.CaseMode}); err != nil {
		return domain.Job{}, false, err
	}
	if err = guard(); err != nil {
		return domain.Job{}, false, err
	}
	return j, false, nil
}

func (s *Store) GetProbeJobSummary(parent context.Context, a domain.Actor, id string) (domain.ProbeJobSummary, error) {
	if parent == nil {
		return domain.ProbeJobSummary{}, domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return domain.ProbeJobSummary{}, domain.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.ProbeJobSummary{}, err
	}
	defer tx.Rollback(ctx)
	j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=$1::uuid`, id))
	if err != nil {
		return domain.ProbeJobSummary{}, err
	}
	r, err := loadProbeRequest(ctx, tx, id)
	if err != nil {
		return domain.ProbeJobSummary{}, err
	}
	out := domain.ProbeJobSummary{JobID: j.ID, LibraryID: j.LibraryID, Phase: domain.ProbeSummaryDisabled}
	if r != nil {
		out.Enabled = true
		out.Scope = r.Intent.Scope
		out.TargetItemID = r.Intent.TargetItemID
		out.Phase = domain.ProbeSummaryWaitingScan
		p, e := loadProbePhase(ctx, tx, id)
		if e != nil && !errors.Is(e, domain.ErrNotFound) {
			return domain.ProbeJobSummary{}, e
		}
		if e == nil {
			if !requestMatchesPhase(r, p) {
				return domain.ProbeJobSummary{}, domain.ErrProbeIdentityMismatch
			}
			out.Phase = p.State
			out.Processed = p.Progress.Processed
			out.Hits = p.Progress.Hits
			out.NegativeHits = p.Progress.NegativeHits
			out.Succeeded = p.Progress.Succeeded
			out.Failed = p.Progress.Failed
			out.Changed = p.Progress.Changed
			out.Unavailable = p.Progress.Unavailable
			out.ErrorCode = string(p.ErrorCode)
		}
		if r.ErrorCode != "" {
			out.Phase = domain.ProbeSummaryAborted
			out.ErrorCode = string(r.ErrorCode)
		}
		if j.State == domain.JobFailed {
			out.Phase = domain.ProbeSummaryAborted
			if out.ErrorCode == "" {
				out.ErrorCode = j.ErrorCode
			}
		}
		if j.State == domain.JobCancelled || j.CancelRequested {
			out.Phase = domain.ProbeSummaryCancelled
			out.ErrorCode = ""
		}
		if j.State == domain.JobSucceeded && out.Phase != domain.ProbeSummaryDone {
			return domain.ProbeJobSummary{}, domain.ErrConflict
		}
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.ProbeJobSummary{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProbeJobSummary{}, storageError(err)
	}
	return out, nil
}
