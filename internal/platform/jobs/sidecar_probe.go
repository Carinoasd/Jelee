package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type sidecarProbeStep int

const (
	sidecarProbeRecord sidecarProbeStep = iota
	sidecarProbeSkip
	sidecarProbeStop
)

// probeSidecars walks the pending external tracks once, by ID, and starts
// at most domain.SidecarProbeMaxPerJob probes. A probe the file itself fails
// is recorded as failed for the stamp that was read, so it is not retried
// until the file changes; a transient outcome (busy, timed out, changed or
// unreadable file) leaves the track pending for the next sync. An
// unavailable runtime ends the pass. Lease and storage errors fail the job.
func (r *Runner) probeSidecars(ctx context.Context, lease domain.JobLease) (storage bool, err error) {
	options := r.options.CatalogSync.SidecarProbes
	if options == nil || options.Repository == nil || options.Prober == nil {
		return false, nil
	}
	timeout := options.FileTimeout
	if timeout <= 0 || timeout > 5*time.Minute {
		timeout = 30 * time.Second
	}
	after, started := "", 0
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if options.Available != nil && !options.Available() {
			return false, nil
		}
		var page []domain.SidecarInspectionTarget
		err = r.ignoreDB(ctx, func(c context.Context) error {
			var err error
			page, err = options.Repository.NextSidecarProbes(c, lease, after, domain.SidecarProbeBatch)
			return err
		})
		if err != nil {
			return true, err
		}
		results := make([]domain.SidecarProbeResult, 0, len(page))
		stop := false
		for _, target := range page {
			if started >= domain.SidecarProbeMaxPerJob {
				stop = true
				break
			}
			started++
			result, step, err := r.sidecarProbe(ctx, options, target, timeout)
			if err != nil {
				return false, err
			}
			if step == sidecarProbeStop {
				stop = true
				break
			}
			if step == sidecarProbeRecord {
				results = append(results, result)
			}
		}
		if len(results) > 0 {
			err = r.ignoreDB(ctx, func(c context.Context) error {
				_, err := options.Repository.RecordSidecarProbes(c, lease, results)
				return err
			})
			if err != nil {
				return true, err
			}
		}
		if stop || len(page) < domain.SidecarProbeBatch {
			return false, nil
		}
		after = page[len(page)-1].ID
	}
}

// sidecarProbe runs one probe and turns it into a recordable result. err is
// set only when the job is cancelled.
func (r *Runner) sidecarProbe(ctx context.Context, options *SidecarProbeOptions, target domain.SidecarInspectionTarget, timeout time.Duration) (domain.SidecarProbeResult, sidecarProbeStep, error) {
	release, err := r.acquireWork(ctx, app.WorkIO)
	if err != nil {
		return domain.SidecarProbeResult{}, sidecarProbeStop, ctx.Err()
	}
	defer release()
	fileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	observation, err := options.Prober.ProbeSidecar(fileCtx, domain.ProbeSource{RootPath: target.RootPath, RelativePath: target.RelativePath})
	if ctx.Err() != nil {
		return domain.SidecarProbeResult{}, sidecarProbeStop, ctx.Err()
	}
	return sidecarProbeOutcome(target, observation, err)
}

// sidecarProbeOutcome classifies one probe. A readable file without an
// audio (or subtitle) stream, unusable output and a verified tool failure
// on this file are its own failures; everything else is not remembered.
func sidecarProbeOutcome(target domain.SidecarInspectionTarget, observation domain.ProbeObservation, err error) (domain.SidecarProbeResult, sidecarProbeStep, error) {
	result := domain.SidecarProbeResult{ID: target.ID, Size: target.Size, ModifiedUnixNano: target.ModifiedUnixNano, State: domain.SidecarProbeFailed}
	switch {
	case err == nil:
		if observation.Stamp.Size != target.Size || observation.Stamp.ModifiedUnixNano != target.ModifiedUnixNano {
			return domain.SidecarProbeResult{}, sidecarProbeSkip, nil
		}
		if facts, ok := domain.SidecarProbeFactsFrom(target.Kind, observation.Metadata); ok {
			result.State, result.Facts = domain.SidecarProbeReady, facts
		}
	case errors.Is(err, domain.ErrProbeFailed), errors.Is(err, domain.ErrProbeMetadataInvalid),
		errors.Is(err, domain.ErrProbeMetadataLimit), errors.Is(err, domain.ErrProbeOutputLimit):
	case errors.Is(err, domain.ErrProbeRuntimeUnavailable), errors.Is(err, domain.ErrProbeIdentityMismatch):
		return domain.SidecarProbeResult{}, sidecarProbeStop, nil
	default:
		return domain.SidecarProbeResult{}, sidecarProbeSkip, nil
	}
	if !domain.ValidSidecarProbeResult(result) {
		return domain.SidecarProbeResult{}, sidecarProbeSkip, nil
	}
	return result, sidecarProbeRecord, nil
}
