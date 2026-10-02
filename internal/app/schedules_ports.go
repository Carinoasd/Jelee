package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"time"
)

// ScheduleCalendar returns one future occurrence. The repository supplies DB
// time, coalescing missed runs without a catch-up queue.
type ScheduleCalendar interface {
	Next(domain.ScheduleTiming, time.Time) (time.Time, error)
}

type ScheduleRepository interface {
	GetScanSchedule(context.Context, domain.Actor, string) (domain.ScanSchedule, error)
	PutScanSchedule(context.Context, domain.Actor, string, domain.ScanScheduleInput, ScheduleCalendar) (domain.ScanSchedule, error)
	DispatchScanSchedule(context.Context, domain.JobPolicy, ScheduleCalendar, *domain.ProbeIdentity, *domain.NFOIdentity, IgnoreAdmissionCapabilities) (bool, error)
}
