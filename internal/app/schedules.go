package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func NewJobsWithSchedules(j *Jobs, repository ScheduleRepository, calendar ScheduleCalendar) (*Jobs, error) {
	if j == nil || repository == nil || calendar == nil {
		return nil, domain.ErrInvalid
	}
	copy := *j
	copy.schedules = repository
	copy.calendar = calendar
	return &copy, nil
}

func (j *Jobs) GetSchedule(ctx context.Context, a domain.Actor, library string) (domain.ScanSchedule, error) {
	if ctx == nil || !validTarget(a, library) {
		return domain.ScanSchedule{}, domain.ErrInvalid
	}
	if j == nil || j.schedules == nil {
		return domain.ScanSchedule{}, domain.ErrNotFound
	}
	return j.schedules.GetScanSchedule(ctx, a, library)
}

func (j *Jobs) PutSchedule(ctx context.Context, a domain.Actor, library string, v domain.ScanScheduleInput) (domain.ScanSchedule, error) {
	if ctx == nil || !validTarget(a, library) {
		return domain.ScanSchedule{}, domain.ErrInvalid
	}
	if j == nil || j.schedules == nil {
		return domain.ScanSchedule{}, domain.ErrNotFound
	}
	if v.Watch && j.watchRepository == nil {
		return domain.ScanSchedule{}, domain.ErrScanUnavailable
	}
	return j.schedules.PutScanSchedule(ctx, a, library, v, j.calendar)
}

// scheduleCapabilities are the admission capabilities a due schedule is
// dispatched with.
func (j *Jobs) scheduleCapabilities() ScheduleCapabilities {
	probe, _ := j.currentProbeIdentity()
	nfo, _ := j.currentNFOIdentity()
	return ScheduleCapabilities{Probe: probe, NFO: nfo, Ignore: j.currentIgnoreCapabilities(),
		MetadataRefresh: j.metadataRefreshJobs != nil && j.metadataRefreshAdmitted(),
		ImageRefresh:    j.imageRefreshJobs != nil && j.imageRefreshAdmit,
		SubtitleCache:   j.maintenanceJobs != nil && j.subtitleCacheAdmit,
		// backupReminderDays is already defaulted by NewJobsWithMaintenance.
		BackupReminderDays: j.backupReminderDays,
	}
}

// DispatchSchedule consumes one persisted due definition using current server
// capabilities. It creates no session and accepts no caller-supplied
// identity: the job runs as the system actor of its schedule.
func (j *Jobs) DispatchSchedule(ctx context.Context) (bool, error) {
	if ctx == nil || j == nil || j.schedules == nil {
		return false, domain.ErrInvalid
	}
	worked, err := j.schedules.DispatchJobSchedule(ctx, j.policy, j.calendar, j.scheduleCapabilities())
	if err != nil || worked {
		return worked, err
	}
	if j.consistency != nil {
		if worked, err = j.consistency.dispatch(ctx, j.policy); err != nil || worked {
			return worked, err
		}
	}
	if j.backupReminder != nil {
		return j.backupReminder.dispatch(ctx, j.policy)
	}
	return false, nil
}

// RunSchedule snapshots the stored options under live administrator authority.
// Manual runs neither enable a disabled definition nor advance its due time.
func (j *Jobs) RunSchedule(ctx context.Context, a domain.Actor, library, key string) (domain.Job, bool, error) {
	v, err := j.GetSchedule(ctx, a, library)
	if err != nil {
		return domain.Job{}, false, err
	}
	return j.SubmitScanOptions(ctx, a, library, key, domain.JobPriorityManual, v.Probe, v.NFO, domain.IgnoreIntent(v.Ignore))
}

// ListJobSchedules lists the generic schedules (G13.1), all or a library's.
func (j *Jobs) ListJobSchedules(ctx context.Context, a domain.Actor, library string) ([]domain.JobSchedule, error) {
	if ctx == nil || !validActor(a) || library != "" && !domain.ValidID(library) {
		return nil, domain.ErrInvalid
	}
	if j == nil || j.schedules == nil {
		return nil, domain.ErrNotFound
	}
	return j.schedules.ListJobSchedules(ctx, a, library)
}

// GetJobSchedule reads one schedule.
func (j *Jobs) GetJobSchedule(ctx context.Context, a domain.Actor, id string) (domain.JobSchedule, error) {
	if ctx == nil || !validActor(a) {
		return domain.JobSchedule{}, domain.ErrInvalid
	}
	if j == nil || j.schedules == nil || !domain.ValidID(id) {
		return domain.JobSchedule{}, domain.ErrNotFound
	}
	return j.schedules.GetJobSchedule(ctx, a, id)
}

// PutJobSchedule creates (id empty) or replaces a schedule. Kinds that cannot
// run unattended are domain.ErrScheduleKindNotAllowed.
func (j *Jobs) PutJobSchedule(ctx context.Context, a domain.Actor, id string, input domain.JobScheduleInput) (domain.JobSchedule, error) {
	if ctx == nil || !validActor(a) || id != "" && !domain.ValidID(id) {
		return domain.JobSchedule{}, domain.ErrInvalid
	}
	if err := domain.ValidateJobScheduleInput(input); err != nil {
		return domain.JobSchedule{}, err
	}
	if j == nil || j.schedules == nil {
		return domain.JobSchedule{}, domain.ErrNotFound
	}
	return j.schedules.PutJobSchedule(ctx, a, id, input, j.calendar)
}

// DeleteJobSchedule removes a schedule under its expected revision.
func (j *Jobs) DeleteJobSchedule(ctx context.Context, a domain.Actor, id string, expectedRevision int64) error {
	if ctx == nil || !validActor(a) || expectedRevision < 1 {
		return domain.ErrInvalid
	}
	if j == nil || j.schedules == nil || !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	return j.schedules.DeleteJobSchedule(ctx, a, id, expectedRevision)
}

// RunJobSchedule submits the schedule's job now as the requesting
// administrator, through the kind's ordinary manual submission: the job is
// theirs, not the system's. The schedule's due time is not advanced.
func (j *Jobs) RunJobSchedule(ctx context.Context, a domain.Actor, id, key string) (domain.Job, bool, error) {
	v, err := j.GetJobSchedule(ctx, a, id)
	if err != nil {
		return domain.Job{}, false, err
	}
	switch v.Kind {
	case domain.JobInventoryScan:
		return j.SubmitScanOptions(ctx, a, v.LibraryID, key, domain.JobPriorityManual, v.Params.Probe, v.Params.NFO, domain.IgnoreIntent(v.Params.Ignore))
	case domain.JobMetadataRefresh:
		return j.SubmitMetadataRefresh(ctx, a, v.LibraryID, key, domain.JobPriorityManual)
	case domain.JobImageRefresh:
		return j.SubmitImageRefresh(ctx, a, v.LibraryID, key, domain.JobPriorityManual)
	case domain.JobSubtitleCacheCleanup:
		return j.SubmitSubtitleCacheCleanup(ctx, a, key, domain.JobPriorityManual, v.Params.MaxIdleDays)
	case domain.JobBackupReminder:
		return j.SubmitBackupReminder(ctx, a, key, domain.JobPriorityManual, v.Params.ThresholdDays)
	}
	// consistency_check has no administrator submission; it runs on its
	// schedule or from the operator command.
	return domain.Job{}, false, domain.ErrScheduleKindNotAllowed
}
