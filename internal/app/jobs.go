package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type Jobs struct {
	schedules             ScheduleRepository
	calendar              ScheduleCalendar
	importRepository      InventoryImportRepository
	importVerifier        InventoryImportVerifier
	cancellationNotifier  JobCancellationNotifier
	ignoreAvailable       func() bool
	familyIgnoreAvailable func() bool
	repository            JobRepository
	policy                domain.JobPolicy
	probeRepository       ProbeJobRepository
	probeIdentity         *domain.ProbeIdentity
	probeCapability       func() domain.ProbeCapability
	scanRepository        ScanJobRepository
	nfoAdmin              NFOAdminRepository
	nfoQueries            NFOQueryRepository
	imageQueries          ImageQueryRepository
	nfoIdentity           *domain.NFOIdentity
	nfoAvailable          func() bool
}

func NewJobs(repository JobRepository, policy domain.JobPolicy) (*Jobs, error) {
	if repository == nil || !validJobPolicy(policy) {
		return nil, domain.ErrInvalid
	}
	return &Jobs{repository: repository, policy: policy}, nil
}

func validJobPolicy(p domain.JobPolicy) bool {
	return p.QueueLimit >= 1 && p.QueueLimit <= 1000 && p.HistoryLimit >= 1 && p.HistoryLimit <= 100 &&
		p.MaxEntries >= 100 && p.MaxEntries <= 500000 && p.MaxDirectories >= 1 && p.MaxDirectories <= 100000 &&
		p.MaxAttempts >= 1 && p.MaxAttempts <= 10 && p.MissingCountLimit >= 1 && p.MissingCountLimit <= 500000 &&
		p.MissingPercentLimit >= 1 && p.MissingPercentLimit <= 100
}

func validJobPage(actor domain.Actor, cursor string, limit int) bool {
	return validActor(actor) && (cursor == "" || domain.ValidID(cursor)) && limit >= 1 && limit <= 100
}

func (j *Jobs) Submit(ctx context.Context, actor domain.Actor, library, key, priority string) (domain.Job, bool, error) {
	return j.SubmitScan(ctx, actor, library, key, priority, false)
}

func (j *Jobs) Retry(ctx context.Context, actor domain.Actor, id, key string) (domain.Job, bool, error) {
	if ctx == nil || !validTarget(actor, id) || !validKey(key) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, false, err
	}
	if j.scanRepository != nil {
		probe, probeErr := j.currentProbeIdentity()
		nfo, nfoErr := j.currentNFOIdentity()
		var job domain.Job
		var replay bool
		var err error
		if capable, ok := j.scanRepository.(FamilyIgnoreAdmissionRepository); ok {
			job, replay, err = capable.RetryScanWithIgnoreFamilies(ctx, actor, id, key, j.policy, probe, nfo, j.currentIgnoreCapabilities())
		} else if capable, ok := j.scanRepository.(IgnoreAdmissionRepository); ok {
			job, replay, err = capable.RetryScanWithIgnoreCapability(ctx, actor, id, key, j.policy, probe, nfo, j.ignoreAvailable != nil && j.ignoreAvailable())
		} else {
			job, replay, err = j.scanRepository.RetryScanWithStages(ctx, actor, id, key, j.policy, probe, nfo)
		}
		return scanAdmissionResult(job, replay, err, probeErr, nfoErr)
	}
	if j.probeRepository != nil {
		identity, capabilityError := j.currentProbeIdentity()
		job, replayed, err := j.probeRepository.RetryScanJob(ctx, actor, id, key, j.policy, identity)
		if err == domain.ErrProbeDisabled && capabilityError != nil {
			err = capabilityError
		}
		return job, replayed, err
	}
	return j.repository.RetryJob(ctx, actor, id, key, j.policy)
}

func (j *Jobs) Cancel(ctx context.Context, actor domain.Actor, id string) (domain.Job, error) {
	if !validTarget(actor, id) {
		return domain.Job{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, err
	}
	job, err := j.repository.CancelJob(ctx, actor, id)
	if err == nil && job.ID == id && job.CancelRequested && job.State == domain.JobRunning && j.cancellationNotifier != nil {
		j.cancellationNotifier.NotifyJobCancellation(id)
	}
	return job, err
}

func (j *Jobs) Get(ctx context.Context, actor domain.Actor, id string) (domain.Job, error) {
	if !validTarget(actor, id) {
		return domain.Job{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, err
	}
	return j.repository.GetJob(ctx, actor, id)
}

func (j *Jobs) List(ctx context.Context, actor domain.Actor, cursor string, limit int, state string) ([]domain.Job, error) {
	if !validJobPage(actor, cursor, limit) {
		return nil, domain.ErrInvalid
	}
	switch state {
	case "", domain.JobQueued, domain.JobRunning, domain.JobSucceeded, domain.JobFailed, domain.JobCancelled:
	default:
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return j.repository.ListJobs(ctx, actor, cursor, limit, state)
}

func (j *Jobs) Entries(ctx context.Context, actor domain.Actor, id, cursor string, limit int) ([]domain.InventoryEntry, error) {
	if !domain.ValidID(id) || !validJobPage(actor, cursor, limit) {
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return j.repository.ListInventory(ctx, actor, id, cursor, limit)
}

func (j *Jobs) Libraries(ctx context.Context, actor domain.Actor, cursor string, limit int) ([]domain.LibrarySummary, error) {
	if !validJobPage(actor, cursor, limit) {
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return j.repository.ListLibraries(ctx, actor, cursor, limit)
}
