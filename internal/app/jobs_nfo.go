package app

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ScanServices binds trusted local readers and repositories. Public submission
// methods accept only intent; callers cannot choose parser or tool identities.
type ScanServices struct {
	Probes          ProbeJobRepository
	ProbeIdentity   *domain.ProbeIdentity
	ProbeCapability func() domain.ProbeCapability
	NFOAdmin        NFOAdminRepository
	NFOQueries      NFOQueryRepository
	Images          ImageQueryRepository
	NFOIdentity     *domain.NFOIdentity
	NFOAvailable    func() bool
}

func NewJobsWithScanStages(repository JobRepository, policy domain.JobPolicy, stages ScanJobRepository, services ScanServices) (*Jobs, error) {
	if stages == nil || services.NFOAdmin == nil || services.NFOQueries == nil || services.Images == nil || services.NFOAvailable == nil {
		return nil, domain.ErrInvalid
	}
	var j *Jobs
	var err error
	if services.Probes != nil {
		j, err = NewJobsWithProbe(repository, policy, services.Probes, services.ProbeIdentity, services.ProbeCapability)
	} else {
		if services.ProbeIdentity != nil || services.ProbeCapability != nil {
			return nil, domain.ErrInvalid
		}
		j, err = NewJobs(repository, policy)
	}
	if err != nil {
		return nil, err
	}
	if services.NFOIdentity != nil {
		if domain.ValidateNFOIdentity(*services.NFOIdentity) != nil {
			return nil, domain.ErrInvalid
		}
		identity := *services.NFOIdentity
		j.nfoIdentity = &identity
	}
	if services.NFOAvailable() && j.nfoIdentity == nil {
		return nil, domain.ErrInvalid
	}
	j.scanRepository, j.nfoAdmin, j.nfoQueries, j.imageQueries = stages, services.NFOAdmin, services.NFOQueries, services.Images
	j.nfoAvailable = services.NFOAvailable
	return j, nil
}

func (j *Jobs) currentNFOIdentity() (*domain.NFOIdentity, error) {
	if j == nil || j.nfoAvailable == nil || !j.nfoAvailable() || j.nfoIdentity == nil {
		return nil, domain.ErrNFOReaderUnavailable
	}
	identity := *j.nfoIdentity
	return &identity, nil
}

func scanAdmissionResult(job domain.Job, replay bool, err, probeErr, nfoErr error) (domain.Job, bool, error) {
	if err == nil {
		return job, replay, nil
	}
	if errors.Is(err, domain.ErrProbeDisabled) && probeErr != nil {
		err = probeErr
	} else if errors.Is(err, domain.ErrNFODisabled) && nfoErr != nil {
		err = nfoErr
	}
	return domain.Job{}, false, err
}

func (j *Jobs) SubmitScanStages(ctx context.Context, actor domain.Actor, library, key, priority string, probe, nfo bool) (domain.Job, bool, error) {
	if ctx == nil || !validTarget(actor, library) || !validKey(key) || priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, false, err
	}
	if j.scanRepository == nil {
		if nfo {
			return domain.Job{}, false, domain.ErrNFOReaderUnavailable
		}
		return j.SubmitScan(ctx, actor, library, key, priority, probe)
	}
	intent := domain.ScanIntent{NFO: nfo}
	var probeIdentity *domain.ProbeIdentity
	var nfoIdentity *domain.NFOIdentity
	var probeErr, nfoErr error
	if probe {
		intent.Probe.Scope = domain.ProbeScopeIncremental
		probeIdentity, probeErr = j.currentProbeIdentity()
	}
	if nfo {
		nfoIdentity, nfoErr = j.currentNFOIdentity()
	}
	// Even an unavailable reader reaches the repository with nil authority so
	// an authorized retained replay can return its original frozen request.
	job, replay, err := j.scanRepository.SubmitScanWithStages(ctx, actor, library, key, priority, intent, j.policy, probeIdentity, nfoIdentity)
	return scanAdmissionResult(job, replay, err, probeErr, nfoErr)
}

func (j *Jobs) NFOPolicy(ctx context.Context, actor domain.Actor, library string) (domain.NFOLibraryPolicy, error) {
	if ctx == nil || !validTarget(actor, library) {
		return domain.NFOLibraryPolicy{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOLibraryPolicy{}, err
	}
	if j.nfoAdmin == nil {
		return domain.NFOLibraryPolicy{}, domain.ErrNFOReaderUnavailable
	}
	return j.nfoAdmin.GetNFOLibraryPolicy(ctx, actor, library)
}

func (j *Jobs) SetNFOPolicy(ctx context.Context, actor domain.Actor, library, key string, expected int64, mode string) (domain.NFOLibraryPolicy, bool, error) {
	if ctx == nil || !validTarget(actor, library) || domain.ValidateNFOPolicyUpdate(library, key, expected, mode) != nil {
		return domain.NFOLibraryPolicy{}, false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOLibraryPolicy{}, false, err
	}
	if j.nfoAdmin == nil {
		return domain.NFOLibraryPolicy{}, false, domain.ErrNFOReaderUnavailable
	}
	return j.nfoAdmin.SetNFOLibraryPolicy(ctx, actor, library, key, expected, mode)
}

func (j *Jobs) NFOSummary(ctx context.Context, actor domain.Actor, job string) (domain.NFOJobSummary, error) {
	if ctx == nil || !validTarget(actor, job) {
		return domain.NFOJobSummary{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOJobSummary{}, err
	}
	if j.nfoQueries == nil {
		return domain.NFOJobSummary{}, domain.ErrNFOReaderUnavailable
	}
	return j.nfoQueries.GetNFOJobSummary(ctx, actor, job)
}

func (j *Jobs) NFOObservations(ctx context.Context, actor domain.Actor, library, cursor string, limit int) (domain.NFOObservationPage, error) {
	if ctx == nil || !validTarget(actor, library) || cursor != "" && !domain.ValidID(cursor) || limit < 1 || limit > domain.NFOObservationPageMax {
		return domain.NFOObservationPage{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOObservationPage{}, err
	}
	identity, err := j.currentNFOIdentity()
	if err != nil || j.nfoQueries == nil {
		return domain.NFOObservationPage{}, domain.ErrNFOReaderUnavailable
	}
	return j.nfoQueries.ListNFOObservations(ctx, actor, library, cursor, limit, *identity)
}

func (j *Jobs) NFOIssues(ctx context.Context, actor domain.Actor, library, observation string, offset, limit int) (domain.NFOIssuesPage, error) {
	if ctx == nil || !validTarget(actor, library) || !domain.ValidID(observation) || offset < 0 || offset > domain.NFOIssuesMax || limit < 1 || limit > domain.NFOIssuesPageMax {
		return domain.NFOIssuesPage{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.NFOIssuesPage{}, err
	}
	identity, err := j.currentNFOIdentity()
	if err != nil || j.nfoQueries == nil {
		return domain.NFOIssuesPage{}, domain.ErrNFOReaderUnavailable
	}
	return j.nfoQueries.GetNFOObservationIssues(ctx, actor, library, observation, offset, limit, *identity)
}

func (j *Jobs) Images(ctx context.Context, actor domain.Actor, job string) (domain.ImageJobSummary, error) {
	if ctx == nil || !validTarget(actor, job) {
		return domain.ImageJobSummary{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.ImageJobSummary{}, err
	}
	if j.imageQueries == nil {
		return domain.ImageJobSummary{}, domain.ErrNFOReaderUnavailable
	}
	return j.imageQueries.GetImageJobSummary(ctx, actor, job)
}
