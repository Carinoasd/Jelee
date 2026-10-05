package jobs

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type inspectedProbe struct {
	entry     domain.ProbeEntry
	candidate domain.ProbeCandidate
	kind      string
}

func (r *Runner) executeProbe(ctx context.Context, lease domain.JobLease, request domain.ProbeRequest) (error, bool) {
	for {
		if ctx.Err() != nil {
			return ctx.Err(), false
		}
		if !r.probeAvailable() {
			return &probeAbort{domain.ProbePhaseRuntimeUnavailable, true}, false
		}
		dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
		page, err := r.probeRepository.NextProbePage(dbCtx, lease, domain.ProbeBatchMax)
		cancel()
		if errors.Is(err, domain.ErrNotFound) {
			dbCtx, cancel = context.WithTimeout(ctx, r.options.DBOperationTimeout)
			phase, err := r.probeRepository.FinishProbePhase(dbCtx, lease)
			cancel()
			if err != nil {
				return probeRepositoryError(err)
			}
			if !probePhaseMatches(phase, request) || phase.State != domain.ProbePhaseDone {
				return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
			}
			return nil, false
		}
		if err != nil {
			return probeRepositoryError(err)
		}
		if domain.ValidateProbePageToken(page.Token) != nil || len(page.Entries) < 1 || len(page.Entries) > domain.ProbeBatchMax {
			return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
		}
		prepared := make([]inspectedProbe, 0, len(page.Entries))
		for i, entry := range page.Entries {
			if !domain.ValidID(entry.Inventory.ID) || !domain.ValidID(entry.Inventory.RootID) || entry.Inventory.Kind != "video" || entry.Source.RootPath == "" || entry.Inventory.Path != entry.Source.RelativePath || i > 0 && entry.Inventory.ID <= page.Entries[i-1].Inventory.ID {
				return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
			}
			if !r.probeAvailable() {
				return &probeAbort{domain.ProbePhaseRuntimeUnavailable, true}, false
			}
			stamp, err := r.inspect(ctx, entry.Source)
			if ctx.Err() != nil {
				return ctx.Err(), false
			}
			item := inspectedProbe{entry: entry, candidate: domain.ProbeCandidate{InventoryID: entry.Inventory.ID, Stamp: stamp}}
			if err != nil {
				item.kind = probeSourceOutcome(err)
				if item.kind == "" {
					return r.unavailableProbe(), false
				}
			} else if stamp.Size != entry.Inventory.Size || stamp.ModifiedUnixNano != entry.Inventory.ModifiedUnixNano {
				item.kind = domain.ProbeCompletionChanged
			}
			if item.kind != "" {
				item.candidate.Stamp = domain.ProbeStamp{}
			}
			prepared = append(prepared, item)
			if item.kind != "" {
				break
			}
		}
		token := page.Token
		for len(prepared) > 0 {
			if ctx.Err() != nil {
				return ctx.Err(), false
			}
			if !r.probeAvailable() {
				return &probeAbort{domain.ProbePhaseRuntimeUnavailable, true}, false
			}
			if prepared[0].kind != "" {
				completion := domain.ProbeCompletion{Candidate: prepared[0].candidate, Kind: prepared[0].kind}
				phase, err := r.commitProbe(ctx, lease, request, token, []domain.ProbeCompletion{completion})
				if err != nil {
					return probeRepositoryError(err)
				}
				token, prepared = phase.Token, prepared[1:]
				continue
			}
			candidates := make([]domain.ProbeCandidate, 0, len(prepared))
			for _, item := range prepared {
				if item.kind != "" {
					break
				}
				candidates = append(candidates, item.candidate)
			}
			dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
			lookups, err := r.probeRepository.LookupProbeBatch(dbCtx, lease, token, candidates)
			cancel()
			if err != nil {
				return probeRepositoryError(err)
			}
			if len(lookups) != len(candidates) {
				return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
			}
			hits := make([]domain.ProbeCompletion, 0, len(lookups))
			for i, lookup := range lookups {
				if lookup.InventoryID != candidates[i].InventoryID {
					return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
				}
				switch lookup.Kind {
				case domain.ProbeLookupHit, domain.ProbeLookupNegativeHit:
					if len(hits) == i {
						hits = append(hits, domain.ProbeCompletion{Candidate: candidates[i], Kind: lookup.Kind})
					}
				case domain.ProbeLookupMiss, domain.ProbeLookupBusy:
				default:
					return &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
				}
			}
			if len(hits) > 0 {
				phase, err := r.commitProbe(ctx, lease, request, token, hits)
				if err != nil {
					return probeRepositoryError(err)
				}
				token, prepared = phase.Token, prepared[len(hits):]
				continue
			}
			if lookups[0].Kind == domain.ProbeLookupBusy {
				if !r.wait(ctx, r.options.PollInterval) {
					return ctx.Err(), false
				}
				break // Fresh observations after any contention wait.
			}
			phase, retry, err, storage := r.probeMiss(ctx, lease, request, token, prepared[0])
			if err != nil {
				if storage {
					return probeRepositoryError(err)
				}
				return err, false
			}
			if retry {
				if !r.wait(ctx, r.options.PollInterval) {
					return ctx.Err(), false
				}
				break
			}
			token, prepared = phase.Token, prepared[1:]
		}
	}
}

func probeSourceOutcome(err error) string {
	switch {
	case errors.Is(err, domain.ErrProbeSourceChanged):
		return domain.ProbeCompletionChanged
	case errors.Is(err, domain.ErrProbeInputUnavailable), errors.Is(err, context.DeadlineExceeded):
		return domain.ProbeCompletionUnavailable
	default:
		return ""
	}
}

func probeFailureCode(err error) domain.ProbeFailureCode {
	switch {
	case errors.Is(err, domain.ErrProbeFailed):
		return domain.ProbeFailureMedia
	case errors.Is(err, domain.ErrProbeMetadataInvalid):
		return domain.ProbeFailureMetadataInvalid
	case errors.Is(err, domain.ErrProbeMetadataLimit):
		return domain.ProbeFailureMetadataLimit
	case errors.Is(err, domain.ErrProbeOutputLimit):
		return domain.ProbeFailureOutputLimit
	case errors.Is(err, context.DeadlineExceeded):
		return domain.ProbeFailureTimeout
	default:
		return ""
	}
}

func (r *Runner) commitProbe(ctx context.Context, lease domain.JobLease, request domain.ProbeRequest, token domain.ProbePageToken, batch []domain.ProbeCompletion) (domain.ProbePhase, error) {
	if ctx.Err() != nil {
		return domain.ProbePhase{}, ctx.Err()
	}
	dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
	defer cancel()
	phase, err := r.probeRepository.CommitProbeBatch(dbCtx, lease, token, batch)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	if !probePhaseMatches(phase, request) || phase.State != domain.ProbePhaseRunning || phase.Token.Revision != token.Revision+1 || phase.Token.AfterID != batch[len(batch)-1].Candidate.InventoryID {
		return domain.ProbePhase{}, domain.ErrProbeIdentityMismatch
	}
	return phase, nil
}

func (r *Runner) probeMiss(ctx context.Context, parent domain.JobLease, request domain.ProbeRequest, token domain.ProbePageToken, item inspectedProbe) (domain.ProbePhase, bool, error, bool) {
	select {
	case r.probeGate <- struct{}{}:
		defer func() { <-r.probeGate }()
	case <-ctx.Done():
		return domain.ProbePhase{}, false, ctx.Err(), false
	}
	if ctx.Err() != nil {
		return domain.ProbePhase{}, false, ctx.Err(), false
	}
	if !r.probeAvailable() {
		return domain.ProbePhase{}, false, &probeAbort{domain.ProbePhaseRuntimeUnavailable, true}, false
	}
	// Queue for CPU before occupying a database child lease. Parent heartbeats
	// remain active while waiting; no shared I/O permit is held here.
	releaseCPU, admissionErr := r.acquireWork(ctx, app.WorkCPU)
	if admissionErr != nil {
		return domain.ProbePhase{}, false, admissionErr, false
	}
	defer func() {
		if releaseCPU != nil {
			releaseCPU()
		}
	}()
	dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
	child, err := r.probeRepository.AcquireProbe(dbCtx, parent, token, item.candidate)
	cancel()
	if errors.Is(err, domain.ErrProbeBusy) || errors.Is(err, domain.ErrConflict) {
		return domain.ProbePhase{}, true, nil, false
	}
	if err != nil {
		return domain.ProbePhase{}, false, err, true
	}
	if domain.ValidateProbeLease(child) != nil || child.JobID != parent.Job.ID || child.Owner != parent.Owner || child.JobGeneration != parent.Generation || child.InventoryID != item.candidate.InventoryID || child.RootID != item.entry.Inventory.RootID || child.Path != item.entry.Inventory.Path || child.Stamp != item.candidate.Stamp || child.Identity != request.Identity || child.LibraryID != request.LibraryID || child.LibraryGeneration != request.LibraryGeneration {
		return domain.ProbePhase{}, false, &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
	}
	// A gate slot is held before acquiring the DB lease. Recheck health after
	// that transaction, so a waiting worker cannot start after runtime failure.
	if !r.probeAvailable() {
		return domain.ProbePhase{}, false, &probeAbort{domain.ProbePhaseRuntimeUnavailable, true}, false
	}
	if r.options.Probe.Prober.IdentityDigest() != r.probeIdentity {
		return domain.ProbePhase{}, false, &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
	}
	observation, err := r.probe(ctx, item.entry.Source)
	releaseCPU()
	releaseCPU = nil
	if ctx.Err() != nil {
		return domain.ProbePhase{}, false, ctx.Err(), false
	}
	if errors.Is(err, domain.ErrProbeBusy) {
		dbCtx, cancel = context.WithTimeout(ctx, r.options.DBOperationTimeout)
		releaseErr := r.probeRepository.ReleaseProbeLease(dbCtx, parent, child)
		cancel()
		return domain.ProbePhase{}, releaseErr == nil, releaseErr, releaseErr != nil
	}
	completion := domain.ProbeCompletion{Candidate: item.candidate, Lease: &child}
	if err != nil {
		if code := probeFailureCode(err); code != "" {
			completion.Kind, completion.FailureCode = domain.ProbeCompletionFailed, code
		} else if kind := probeSourceOutcome(err); kind != "" {
			completion.Kind = kind
		} else {
			return domain.ProbePhase{}, false, r.unavailableProbe(), false
		}
	} else {
		if observation.IdentityDigest != request.Identity.Digest {
			_ = r.unavailableProbe()
			return domain.ProbePhase{}, false, &probeAbort{domain.ProbePhaseIdentityMismatch, true}, false
		}
		if domain.ValidateProbeStamp(observation.Stamp) != nil {
			return domain.ProbePhase{}, false, r.unavailableProbe(), false
		}
		if observation.Stamp != item.candidate.Stamp {
			completion.Kind = domain.ProbeCompletionChanged
		} else if _, err := domain.MarshalProbeMetadata(observation.Metadata); err != nil {
			return domain.ProbePhase{}, false, r.unavailableProbe(), false
		} else {
			completion.Kind, completion.Metadata = domain.ProbeCompletionSucceeded, &observation.Metadata
		}
	}
	if completion.Kind == domain.ProbeCompletionSucceeded || completion.Kind == domain.ProbeCompletionFailed {
		stamp, err := r.inspect(ctx, item.entry.Source)
		if ctx.Err() != nil {
			return domain.ProbePhase{}, false, ctx.Err(), false
		}
		if err != nil {
			completion.Kind = probeSourceOutcome(err)
			if completion.Kind == "" {
				return domain.ProbePhase{}, false, r.unavailableProbe(), false
			}
		} else if stamp != item.candidate.Stamp {
			completion.Kind = domain.ProbeCompletionChanged
		}
		if completion.Kind != domain.ProbeCompletionSucceeded && completion.Kind != domain.ProbeCompletionFailed {
			completion.Metadata, completion.FailureCode = nil, ""
		}
	}
	if !r.probeAvailable() {
		return domain.ProbePhase{}, false, &probeAbort{domain.ProbePhaseRuntimeUnavailable, true}, false
	}
	phase, err := r.commitProbe(ctx, parent, request, token, []domain.ProbeCompletion{completion})
	return phase, false, err, err != nil
}
