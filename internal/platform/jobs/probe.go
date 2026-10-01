package jobs

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ProbeOptions is trusted local wiring. It accepts no executable or arguments.
// Available must be concurrency safe; a runtime failure also latches this Runner
// unavailable even if the external callback has not yet updated its projection.
type ProbeOptions struct {
	Repository           app.ProbeExecutionRepository
	Prober               app.MetadataProber
	LeaseDuration        time.Duration
	MaxConcurrent        int
	FileTimeout          time.Duration // zero uses 30 seconds; independent per readonly call
	Available            func() bool
	OnRuntimeUnavailable func()
}

type probeClaimer interface {
	ClaimJobWithProbe(context.Context, string, bool, time.Duration, bool) (domain.JobLease, error)
}

type probeAbort struct {
	code    domain.ProbePhaseError
	persist bool
}

func (e *probeAbort) Error() string { return string(e.code) }

func (r *Runner) configureProbe() error {
	r.probeRepository, _ = r.repository.(app.ProbeExecutionRepository)
	if r.options.Probe == nil {
		return nil
	}
	p := *r.options.Probe
	if p.FileTimeout == 0 {
		p.FileTimeout = 30 * time.Second
	}
	if p.Repository == nil || p.Prober == nil || p.LeaseDuration < 10*time.Second || p.LeaseDuration > 5*time.Minute || p.MaxConcurrent < 1 || p.MaxConcurrent > 8 || p.FileTimeout < time.Second || p.FileTimeout > 5*time.Minute {
		return domain.ErrInvalid
	}
	digest := p.Prober.IdentityDigest()
	if _, err := hex.DecodeString(digest); err != nil || len(digest) != 64 || strings.ToLower(digest) != digest {
		return domain.ErrInvalid
	}
	r.options.Probe = &p
	if r.options.DBOperationTimeout >= r.heartbeatInterval() {
		return domain.ErrInvalid
	}
	r.probeRepository, r.probeIdentity, r.probeGate = p.Repository, digest, make(chan struct{}, p.MaxConcurrent)
	return nil
}

func (r *Runner) heartbeatInterval() time.Duration {
	lease := r.options.LeaseDuration
	if r.options.Probe != nil && r.options.Probe.LeaseDuration < lease {
		lease = r.options.Probe.LeaseDuration
	}
	return lease / 3
}

func (r *Runner) probeAvailable() bool {
	p := r.options.Probe
	return p != nil && !r.probeUnavailable.Load() && (p.Available == nil || p.Available())
}

func (r *Runner) unavailableProbe() error {
	if r.probeUnavailable.CompareAndSwap(false, true) && r.options.Probe != nil && r.options.Probe.OnRuntimeUnavailable != nil {
		func() {
			defer func() {
				if recover() != nil {
					r.logger.Warn("probe capability callback failed", "component", "jobs", "code", "probe_callback_failed")
				}
			}()
			r.options.Probe.OnRuntimeUnavailable()
		}()
	}
	return &probeAbort{code: domain.ProbePhaseRuntimeUnavailable, persist: true}
}

func probeRepositoryError(err error) (error, bool) {
	switch {
	case errors.Is(err, domain.ErrProbeCacheCapacity):
		return &probeAbort{domain.ProbePhaseCapacity, true}, false
	case errors.Is(err, domain.ErrProbeInvalidated):
		return &probeAbort{domain.ProbePhaseInvalidated, true}, false
	case errors.Is(err, domain.ErrProbeIdentityMismatch):
		return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
	default:
		return err, true
	}
}

func (r *Runner) executeProbeOnly(ctx context.Context, lease domain.JobLease) (result error, repositoryError bool) {
	defer func() {
		if recover() != nil {
			result, repositoryError = domain.ErrScanIO, false
		}
	}()
	if r.probeRepository == nil {
		return r.executeInventory(ctx, lease)
	}
	dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
	work, err := r.probeRepository.LoadProbeWork(dbCtx, lease)
	cancel()
	if err != nil {
		return probeRepositoryError(err)
	}
	if work.Request == nil {
		if work.Phase != nil {
			return &probeAbort{code: domain.ProbePhaseIdentityMismatch}, false
		}
		return r.executeInventory(ctx, lease)
	}
	request := *work.Request
	start := domain.ProbePhaseStart{Identity: request.Identity, Scope: request.Intent.Scope, TargetItemID: request.Intent.TargetItemID}
	if request.JobID != lease.Job.ID || request.LibraryID != lease.Job.LibraryID || request.LibraryGeneration < 1 || domain.ValidateProbePhaseStart(start) != nil || request.Intent.TargetItemID == "" && request.TargetItemGeneration != 0 || request.Intent.TargetItemID != "" && request.TargetItemGeneration < 1 {
		return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
	}
	if request.ErrorCode != "" {
		if !domain.ValidProbePhaseError(request.ErrorCode) {
			return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
		}
		return &probeAbort{code: request.ErrorCode}, false
	}
	if work.Phase != nil {
		p := work.Phase
		if !probePhaseMatches(*p, request) {
			return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
		}
		switch p.State {
		case domain.ProbePhaseDone:
			return nil, false
		case domain.ProbePhaseAborted:
			if !domain.ValidProbePhaseError(p.ErrorCode) {
				return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
			}
			return &probeAbort{p.ErrorCode, true}, false
		case domain.ProbePhaseRunning:
		default:
			return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
		}
	}
	if !r.probeAvailable() {
		return &probeAbort{domain.ProbePhaseRuntimeUnavailable, true}, false
	}
	if request.Identity.Digest != r.probeIdentity {
		return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
	}
	if work.Phase == nil {
		if err, storage := r.executeInventory(ctx, lease); err != nil {
			return err, storage
		}
		dbCtx, cancel = context.WithTimeout(ctx, r.options.DBOperationTimeout)
		phase, err := r.probeRepository.BeginRequestedProbePhase(dbCtx, lease)
		cancel()
		if err != nil {
			return probeRepositoryError(err)
		}
		if !probePhaseMatches(phase, request) || phase.State != domain.ProbePhaseRunning {
			return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
		}
	}
	return r.executeProbe(ctx, lease, request)
}

func probePhaseMatches(p domain.ProbePhase, request domain.ProbeRequest) bool {
	return p.JobID == request.JobID && p.LibraryID == request.LibraryID && p.Start == (domain.ProbePhaseStart{Identity: request.Identity, Scope: request.Intent.Scope, TargetItemID: request.Intent.TargetItemID}) && p.LibraryGeneration == request.LibraryGeneration && p.TargetItemGeneration == request.TargetItemGeneration
}

func (r *Runner) inspect(ctx context.Context, source domain.ProbeSource) (stamp domain.ProbeStamp, result error) {
	defer func() {
		if recover() != nil {
			stamp, result = domain.ProbeStamp{}, domain.ErrProbeRuntimeUnavailable
		}
	}()
	fileCtx, cancel := context.WithTimeout(ctx, r.options.Probe.FileTimeout)
	defer cancel()
	stamp, result = r.options.Probe.Prober.Inspect(fileCtx, source)
	if ctx.Err() != nil {
		return domain.ProbeStamp{}, ctx.Err()
	}
	if fileCtx.Err() != nil {
		return domain.ProbeStamp{}, fileCtx.Err()
	}
	if result != nil {
		return domain.ProbeStamp{}, result
	}
	if domain.ValidateProbeStamp(stamp) != nil {
		return domain.ProbeStamp{}, domain.ErrProbeRuntimeUnavailable
	}
	return stamp, nil
}

func (r *Runner) probe(ctx context.Context, source domain.ProbeSource) (observation domain.ProbeObservation, result error) {
	defer func() {
		if recover() != nil {
			observation, result = domain.ProbeObservation{}, domain.ErrProbeRuntimeUnavailable
		}
	}()
	fileCtx, cancel := context.WithTimeout(ctx, r.options.Probe.FileTimeout)
	defer cancel()
	observation, result = r.options.Probe.Prober.Probe(fileCtx, source)
	if ctx.Err() != nil {
		return domain.ProbeObservation{}, ctx.Err()
	}
	if fileCtx.Err() != nil {
		return domain.ProbeObservation{}, fileCtx.Err()
	}
	if result != nil {
		return domain.ProbeObservation{}, result
	}
	return observation, nil
}
