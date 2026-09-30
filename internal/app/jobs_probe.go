package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NewJobsWithProbe keeps runtime authority local. Callers cannot supply an
// identity through SubmitScan or Rebuild; the constructor copies trusted pins.
func NewJobsWithProbe(repository JobRepository, policy domain.JobPolicy, probes ProbeJobRepository, identity *domain.ProbeIdentity, capability func() domain.ProbeCapability) (*Jobs, error) {
	j, err := NewJobs(repository, policy)
	if err != nil {
		return nil, err
	}
	if probes == nil || capability == nil {
		return nil, domain.ErrInvalid
	}
	if identity != nil {
		if domain.ValidateProbeIdentity(*identity) != nil {
			return nil, domain.ErrInvalid
		}
		value := *identity
		j.probeIdentity = &value
	}
	initial := capability()
	if initial.Available && (identity == nil || !initial.Enabled) {
		return nil, domain.ErrInvalid
	}
	j.probeRepository, j.probeCapability = probes, capability
	return j, nil
}

func (j *Jobs) ProbeCapability() domain.ProbeCapability {
	if j == nil || j.probeCapability == nil {
		return domain.ProbeCapability{State: "disabled", Reason: "configuration_disabled"}
	}
	return j.probeCapability()
}

func (j *Jobs) currentProbeIdentity() (*domain.ProbeIdentity, error) {
	capability := j.ProbeCapability()
	if !capability.Enabled {
		return nil, domain.ErrProbeDisabled
	}
	if !capability.Available || j.probeIdentity == nil {
		return nil, domain.ErrProbeRuntimeUnavailable
	}
	value := *j.probeIdentity
	return &value, nil
}

func (j *Jobs) SubmitScan(ctx context.Context, actor domain.Actor, library, key, priority string, probe bool) (domain.Job, bool, error) {
	if !validTarget(actor, library) || !validKey(key) || priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, false, err
	}
	intent := domain.ProbeIntent{}
	var identity *domain.ProbeIdentity
	var capabilityErr error
	if probe {
		identity, capabilityErr = j.currentProbeIdentity()
		intent.Scope = domain.ProbeScopeIncremental
	}
	if j.probeRepository == nil {
		if probe {
			return domain.Job{}, false, capabilityErr
		}
		return j.repository.SubmitJob(ctx, actor, library, key, priority, j.policy)
	}
	job, replay, err := j.probeRepository.SubmitScanJob(ctx, actor, library, key, priority, intent, j.policy, identity)
	if err == domain.ErrProbeDisabled && capabilityErr != nil {
		err = capabilityErr
	}
	return job, replay, err
}

func (j *Jobs) RebuildProbe(ctx context.Context, actor domain.Actor, target, key, priority string, item bool) (domain.Job, bool, error) {
	if !validTarget(actor, target) || !validKey(key) || priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, false, err
	}
	identity, capabilityErr := j.currentProbeIdentity()
	if j.probeRepository == nil {
		return domain.Job{}, false, domain.ErrProbeDisabled
	}
	library := target
	intent := domain.ProbeIntent{Scope: domain.ProbeScopeLibraryRebuild}
	if item {
		library = ""
		intent = domain.ProbeIntent{Scope: domain.ProbeScopeItemRebuild, TargetItemID: target}
	}
	job, replay, err := j.probeRepository.SubmitScanJob(ctx, actor, library, key, priority, intent, j.policy, identity)
	if err == domain.ErrProbeDisabled && capabilityErr != nil {
		err = capabilityErr
	}
	return job, replay, err
}

func (j *Jobs) ProbeSummary(ctx context.Context, actor domain.Actor, id string) (domain.ProbeJobSummary, error) {
	if !validTarget(actor, id) {
		return domain.ProbeJobSummary{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.ProbeJobSummary{}, err
	}
	if j.probeRepository != nil {
		return j.probeRepository.GetProbeJobSummary(ctx, actor, id)
	}
	job, err := j.repository.GetJob(ctx, actor, id)
	if err != nil {
		return domain.ProbeJobSummary{}, err
	}
	return domain.ProbeJobSummary{JobID: job.ID, LibraryID: job.LibraryID, Phase: domain.ProbeSummaryDisabled}, nil
}
