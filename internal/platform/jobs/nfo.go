package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type NFOOptions struct {
	Repository           app.NFOExecutionRepository
	Reader               app.NFOReader
	MaxConcurrent        int
	FileTimeout          time.Duration
	Available            func() bool
	OnRuntimeUnavailable func()
}

type stagesClaimer interface {
	ClaimJobWithCapabilities(context.Context, string, bool, time.Duration, domain.ScanCapabilities) (domain.JobLease, error)
}
type nfoAbort struct {
	code    domain.NFOPhaseError
	persist bool
}

func (e *nfoAbort) Error() string { return string(e.code) }

func (r *Runner) configureNFO() error {
	r.nfoRepository, _ = r.repository.(app.NFOExecutionRepository)
	if r.options.NFO == nil {
		return nil
	}
	n := *r.options.NFO
	if n.FileTimeout == 0 {
		n.FileTimeout = 30 * time.Second
	}
	if n.Repository == nil || n.Reader == nil || n.MaxConcurrent < 1 || n.MaxConcurrent > 2 || n.FileTimeout < time.Second || n.FileTimeout > 5*time.Minute {
		return domain.ErrInvalid
	}
	identity := n.Reader.Identity()
	if domain.ValidateNFOIdentity(identity) != nil {
		return domain.ErrInvalid
	}
	r.options.NFO = &n
	r.nfoRepository, r.nfoIdentity, r.nfoGate = n.Repository, identity, make(chan struct{}, n.MaxConcurrent)
	return nil
}
func (r *Runner) nfoAvailable() bool {
	n := r.options.NFO
	return n != nil && !r.nfoUnavailable.Load() && (n.Available == nil || n.Available())
}
func (r *Runner) unavailableNFO() error {
	if r.nfoUnavailable.CompareAndSwap(false, true) && r.options.NFO != nil && r.options.NFO.OnRuntimeUnavailable != nil {
		func() {
			defer func() {
				if recover() != nil {
					r.logger.Warn("nfo capability callback failed", "component", "jobs", "code", "nfo_callback_failed")
				}
			}()
			r.options.NFO.OnRuntimeUnavailable()
		}()
	}
	return &nfoAbort{domain.NFOPhaseUnavailable, true}
}
func nfoRepositoryError(err error) (error, bool) {
	switch {
	case errors.Is(err, domain.ErrNFOCacheCapacity):
		return &nfoAbort{domain.NFOPhaseCapacity, true}, false
	case errors.Is(err, domain.ErrNFOInvalidated):
		return &nfoAbort{domain.NFOPhaseInvalidated, true}, false
	case errors.Is(err, domain.ErrNFOIdentityMismatch):
		return &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
	default:
		return err, true
	}
}
func nfoPhaseMatches(p domain.NFOPhase, q domain.NFORequest) bool {
	return p.JobID == q.JobID && p.LibraryID == q.LibraryID && p.Mode == q.Mode && p.Identity == q.Identity && p.IdentityDigest == q.IdentityDigest && p.LibraryGeneration == q.LibraryGeneration
}
func frozenOffNFO(p *domain.NFOPhase) bool {
	return p != nil && p.Mode == domain.NFOModeOff && p.State == domain.NFOPhaseAborted && p.ErrorCode == domain.NFOPhaseDisabled && p.Progress == (domain.NFOProgress{}) && p.Token.AfterID == ""
}
func validateNFOWork(l domain.JobLease, w domain.NFOWork) error {
	bad := func(persist bool) error { return &nfoAbort{domain.NFOPhaseIdentityMismatch, persist} }
	if w.Request == nil {
		if w.Phase == nil || frozenOffNFO(w.Phase) && w.Phase.JobID == l.Job.ID && w.Phase.LibraryID == l.Job.LibraryID {
			return nil
		}
		return bad(false)
	}
	q := *w.Request
	if q.JobID != l.Job.ID || q.LibraryID != l.Job.LibraryID || q.LibraryGeneration < 1 || w.Phase == nil || !nfoPhaseMatches(*w.Phase, q) {
		return bad(true)
	}
	if !q.Requested {
		if q.Mode != domain.NFOModeOff || q.ErrorCode != "" || !frozenOffNFO(w.Phase) {
			return bad(true)
		}
		return nil
	}
	if q.Mode != domain.NFOModeReadOnly {
		return bad(true)
	}
	if q.ErrorCode != "" {
		if !domain.ValidNFOPhaseError(q.ErrorCode) {
			return bad(false)
		}
		return &nfoAbort{q.ErrorCode, false}
	}
	if w.Phase.State == domain.NFOPhaseAborted {
		if !domain.ValidNFOPhaseError(w.Phase.ErrorCode) {
			return bad(false)
		}
		return &nfoAbort{w.Phase.ErrorCode, true}
	}
	if domain.ValidateNFOPhase(*w.Phase) != nil {
		return bad(w.Phase.State != domain.NFOPhaseDone)
	}
	return nil
}

// Load both phases before scanning: a completed phase is proof that inventory
// is frozen. In particular, a completed probe must not bypass unfinished NFO.
func (r *Runner) execute(ctx context.Context, l domain.JobLease) (result error, storage bool) {
	if r.nfoRepository == nil {
		return r.executeProbeOnly(ctx, l)
	}
	defer func() {
		if recover() != nil {
			result, storage = domain.ErrScanIO, false
		}
	}()
	dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
	nfo, err := r.nfoRepository.LoadNFOWork(dbCtx, l)
	cancel()
	if err != nil {
		return nfoRepositoryError(err)
	}
	if err := validateNFOWork(l, nfo); err != nil {
		return err, false
	}
	var probe domain.ProbeWork
	if r.probeRepository != nil {
		dbCtx, cancel = context.WithTimeout(ctx, r.options.DBOperationTimeout)
		probe, err = r.probeRepository.LoadProbeWork(dbCtx, l)
		cancel()
		if err != nil {
			return probeRepositoryError(err)
		}
	}
	if err := validateStageProbe(l, probe); err != nil {
		return err, false
	}
	nfoRequested := nfo.Request != nil && nfo.Request.Requested
	if probe.Phase != nil && nfoRequested && nfo.Phase.State != domain.NFOPhaseDone {
		return &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
	}
	frozen := probe.Phase != nil || nfoRequested && (nfo.Phase.State == domain.NFOPhaseRunning || nfo.Phase.State == domain.NFOPhaseDone)
	if !frozen {
		if err, storage := r.executeInventory(ctx, l); err != nil {
			return err, storage
		}
	}
	if nfoRequested && nfo.Phase.State != domain.NFOPhaseDone {
		if !r.nfoAvailable() {
			return &nfoAbort{domain.NFOPhaseUnavailable, true}, false
		}
		if nfo.Request.Identity != r.nfoIdentity || r.options.NFO.Reader.Identity() != r.nfoIdentity {
			return &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
		}
		if nfo.Phase.State == domain.NFOPhaseWaiting {
			dbCtx, cancel = context.WithTimeout(ctx, r.options.DBOperationTimeout)
			phase, err := r.nfoRepository.BeginRequestedNFOPhase(dbCtx, l)
			cancel()
			if err != nil {
				return nfoRepositoryError(err)
			}
			if !nfoPhaseMatches(phase, *nfo.Request) || domain.ValidateNFOPhase(phase) != nil || phase.State != domain.NFOPhaseRunning {
				return &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
			}
		}
		if err, storage := r.executeNFO(ctx, l, *nfo.Request); err != nil {
			return err, storage
		}
	}
	if probe.Request == nil || probe.Phase != nil && probe.Phase.State == domain.ProbePhaseDone {
		return nil, false
	}
	if !r.probeAvailable() {
		return &probeAbort{domain.ProbePhaseRuntimeUnavailable, true}, false
	}
	if probe.Request.Identity.Digest != r.probeIdentity {
		return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
	}
	if probe.Phase == nil {
		dbCtx, cancel = context.WithTimeout(ctx, r.options.DBOperationTimeout)
		phase, err := r.probeRepository.BeginRequestedProbePhase(dbCtx, l)
		cancel()
		if err != nil {
			return probeRepositoryError(err)
		}
		if !probePhaseMatches(phase, *probe.Request) || phase.State != domain.ProbePhaseRunning {
			return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
		}
	}
	return r.executeProbe(ctx, l, *probe.Request)
}

func validateStageProbe(l domain.JobLease, w domain.ProbeWork) error {
	bad := func(persist bool) error { return &probeAbort{domain.ProbePhaseIdentityMismatch, persist} }
	if w.Request == nil {
		if w.Phase != nil {
			return bad(false)
		}
		return nil
	}
	q := *w.Request
	start := domain.ProbePhaseStart{Identity: q.Identity, Scope: q.Intent.Scope, TargetItemID: q.Intent.TargetItemID}
	if q.JobID != l.Job.ID || q.LibraryID != l.Job.LibraryID || q.LibraryGeneration < 1 || domain.ValidateProbePhaseStart(start) != nil || q.Intent.TargetItemID == "" && q.TargetItemGeneration != 0 || q.Intent.TargetItemID != "" && q.TargetItemGeneration < 1 {
		return bad(true)
	}
	if q.ErrorCode != "" {
		if !domain.ValidProbePhaseError(q.ErrorCode) {
			return bad(false)
		}
		return &probeAbort{q.ErrorCode, false}
	}
	if w.Phase == nil {
		return nil
	}
	if !probePhaseMatches(*w.Phase, q) {
		return bad(true)
	}
	switch w.Phase.State {
	case domain.ProbePhaseRunning, domain.ProbePhaseDone:
		return nil
	case domain.ProbePhaseAborted:
		if domain.ValidProbePhaseError(w.Phase.ErrorCode) {
			return &probeAbort{w.Phase.ErrorCode, true}
		}
	}
	return bad(true)
}
