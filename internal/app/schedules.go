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

// DispatchSchedule consumes one persisted due definition using current server
// capabilities. It creates no session and accepts no caller-supplied identity.
func (j *Jobs) DispatchSchedule(ctx context.Context) (bool, error) {
	if ctx == nil || j == nil || j.schedules == nil {
		return false, domain.ErrInvalid
	}
	probe, _ := j.currentProbeIdentity()
	nfo, _ := j.currentNFOIdentity()
	worked, err := j.schedules.DispatchScanSchedule(ctx, j.policy, j.calendar, probe, nfo, j.currentIgnoreCapabilities())
	if err != nil || worked || j.consistency == nil {
		return worked, err
	}
	return j.consistency.dispatch(ctx, j.policy)
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
