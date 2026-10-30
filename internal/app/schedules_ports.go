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

// ScheduleCapabilities are this server's admission capabilities when a due
// schedule is dispatched. An occurrence of a kind this server cannot run is
// skipped with kind_unavailable; a temporarily unavailable scan capability
// keeps the occurrence for a retry.
type ScheduleCapabilities struct {
	Probe           *domain.ProbeIdentity
	NFO             *domain.NFOIdentity
	Ignore          IgnoreAdmissionCapabilities
	MetadataRefresh bool
	ImageRefresh    bool
	SubtitleCache   bool
	// BackupReminderDays is the threshold of a schedule without its own.
	BackupReminderDays int
}

type ScheduleRepository interface {
	GetScanSchedule(context.Context, domain.Actor, string) (domain.ScanSchedule, error)
	PutScanSchedule(context.Context, domain.Actor, string, domain.ScanScheduleInput, ScheduleCalendar) (domain.ScanSchedule, error)
	// DispatchJobSchedule queues the job of at most one due schedule as the
	// system actor and advances it; worked reports that a schedule changed.
	DispatchJobSchedule(context.Context, domain.JobPolicy, ScheduleCalendar, ScheduleCapabilities) (bool, error)
	ListJobSchedules(ctx context.Context, actor domain.Actor, library string) ([]domain.JobSchedule, error)
	GetJobSchedule(ctx context.Context, actor domain.Actor, id string) (domain.JobSchedule, error)
	// PutJobSchedule creates (id empty) or replaces a schedule.
	PutJobSchedule(ctx context.Context, actor domain.Actor, id string, input domain.JobScheduleInput, calendar ScheduleCalendar) (domain.JobSchedule, error)
	DeleteJobSchedule(ctx context.Context, actor domain.Actor, id string, expectedRevision int64) error
}
