package app

import (
	"context"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// MaintenanceJobRepository admits and reports the maintenance jobs of G13.6
// and G14.7: the extraction cache cleanup, the backup reminder and the
// removal of a library's external metadata.
type MaintenanceJobRepository interface {
	SubmitSubtitleCacheCleanupJob(ctx context.Context, actor domain.Actor, key, priority string, maxIdleDays int, policy domain.JobPolicy) (domain.Job, bool, error)
	GetSubtitleCacheCleanupState(ctx context.Context, actor domain.Actor, job string) (domain.SubtitleCacheCleanupJobState, error)
	SubmitBackupReminderJob(ctx context.Context, actor domain.Actor, key, priority string, days int, policy domain.JobPolicy) (domain.Job, bool, error)
	GetBackupReminderState(ctx context.Context, actor domain.Actor, job string) (domain.BackupReminderJobState, error)
	BackupStatus(ctx context.Context, actor domain.Actor, days int) (domain.BackupStatus, error)
	SubmitExternalMetadataRemovalJob(ctx context.Context, actor domain.Actor, library, key string, input domain.ExternalMetadataRemovalInput, policy domain.JobPolicy) (domain.Job, bool, error)
	GetExternalMetadataRemovalState(ctx context.Context, actor domain.Actor, job string) (domain.ExternalMetadataRemovalJobState, error)
}

// NewJobsWithMaintenance lets base run the maintenance jobs through
// repository. subtitleCache admits cache cleanups (a configured, private
// extraction cache); without it a submission is
// domain.ErrSubtitleCacheUnavailable. backupDays is the default backup
// reminder threshold, zero for domain.BackupReminderDaysDefault.
func NewJobsWithMaintenance(base *Jobs, repository MaintenanceJobRepository, subtitleCache bool, backupDays int) (*Jobs, error) {
	if base == nil || repository == nil || backupDays != 0 && !domain.ValidBackupReminderDays(backupDays) {
		return nil, domain.ErrInvalid
	}
	if backupDays == 0 {
		backupDays = domain.BackupReminderDaysDefault
	}
	value := *base
	value.maintenanceJobs, value.subtitleCacheAdmit, value.backupReminderDays = repository, subtitleCache, backupDays
	return &value, nil
}

func (j *Jobs) maintenanceRepository(ctx context.Context) (MaintenanceJobRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if j.maintenanceJobs == nil {
		return nil, domain.ErrNotFound
	}
	return j.maintenanceJobs, nil
}

func validPriority(priority string) bool {
	return priority == domain.JobPriorityManual || priority == domain.JobPriorityBackground
}

// SubmitSubtitleCacheCleanup queues a cleanup of the extraction cache;
// maxIdleDays zero selects domain.SubtitleCacheIdleDaysDefault.
func (j *Jobs) SubmitSubtitleCacheCleanup(ctx context.Context, actor domain.Actor, key, priority string, maxIdleDays int) (domain.Job, bool, error) {
	if maxIdleDays == 0 {
		maxIdleDays = domain.SubtitleCacheIdleDaysDefault
	}
	if ctx == nil || j == nil || !validActor(actor) || !validKey(key) || !validPriority(priority) || maxIdleDays < 1 || maxIdleDays > domain.SubtitleCacheIdleDaysMax {
		return domain.Job{}, false, domain.ErrInvalid
	}
	repository, err := j.maintenanceRepository(ctx)
	if err != nil {
		return domain.Job{}, false, err
	}
	if !j.subtitleCacheAdmit {
		return domain.Job{}, false, domain.ErrSubtitleCacheUnavailable
	}
	return repository.SubmitSubtitleCacheCleanupJob(ctx, actor, key, priority, maxIdleDays, j.policy)
}

// SubtitleCacheCleanupState returns the progress of a cleanup job; any other
// kind is domain.ErrNotFound.
func (j *Jobs) SubtitleCacheCleanupState(ctx context.Context, actor domain.Actor, job string) (domain.SubtitleCacheCleanupJobState, error) {
	if ctx == nil || j == nil || !validTarget(actor, job) {
		return domain.SubtitleCacheCleanupJobState{}, domain.ErrInvalid
	}
	repository, err := j.maintenanceRepository(ctx)
	if err != nil {
		return domain.SubtitleCacheCleanupJobState{}, err
	}
	return repository.GetSubtitleCacheCleanupState(ctx, actor, job)
}

// SubmitBackupReminder queues a backup check; days zero selects the
// configured threshold.
func (j *Jobs) SubmitBackupReminder(ctx context.Context, actor domain.Actor, key, priority string, days int) (domain.Job, bool, error) {
	if ctx == nil || j == nil || !validActor(actor) || !validKey(key) || !validPriority(priority) || days != 0 && !domain.ValidBackupReminderDays(days) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	repository, err := j.maintenanceRepository(ctx)
	if err != nil {
		return domain.Job{}, false, err
	}
	if days == 0 {
		days = j.backupReminderDays
	}
	return repository.SubmitBackupReminderJob(ctx, actor, key, priority, days, j.policy)
}

// BackupReminderState returns the result of a backup reminder job.
func (j *Jobs) BackupReminderState(ctx context.Context, actor domain.Actor, job string) (domain.BackupReminderJobState, error) {
	if ctx == nil || j == nil || !validTarget(actor, job) {
		return domain.BackupReminderJobState{}, domain.ErrInvalid
	}
	repository, err := j.maintenanceRepository(ctx)
	if err != nil {
		return domain.BackupReminderJobState{}, err
	}
	return repository.GetBackupReminderState(ctx, actor, job)
}

// BackupStatus evaluates the last metadata export against the configured
// threshold without queueing a job.
func (j *Jobs) BackupStatus(ctx context.Context, actor domain.Actor) (domain.BackupStatus, error) {
	if ctx == nil || j == nil || !validActor(actor) {
		return domain.BackupStatus{}, domain.ErrInvalid
	}
	repository, err := j.maintenanceRepository(ctx)
	if err != nil {
		return domain.BackupStatus{}, err
	}
	return repository.BackupStatus(ctx, actor, j.backupReminderDays)
}

// SubmitExternalMetadataRemoval queues the removal of the external metadata
// of one library. It changes metadata of many items, so input.Confirm must
// be true (domain.ErrConfirmationRequired otherwise). Removal needs no
// provider: it stays available after a TMDB key is withdrawn.
func (j *Jobs) SubmitExternalMetadataRemoval(ctx context.Context, actor domain.Actor, library, key string, input domain.ExternalMetadataRemovalInput) (domain.Job, bool, error) {
	if ctx == nil || j == nil || !validTarget(actor, library) || !validKey(key) || !validPriority(input.Priority) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if !input.Confirm {
		return domain.Job{}, false, domain.ErrConfirmationRequired
	}
	repository, err := j.maintenanceRepository(ctx)
	if err != nil {
		return domain.Job{}, false, err
	}
	return repository.SubmitExternalMetadataRemovalJob(ctx, actor, library, key, input, j.policy)
}

// ExternalMetadataRemovalState returns the progress of a removal job.
func (j *Jobs) ExternalMetadataRemovalState(ctx context.Context, actor domain.Actor, job string) (domain.ExternalMetadataRemovalJobState, error) {
	if ctx == nil || j == nil || !validTarget(actor, job) {
		return domain.ExternalMetadataRemovalJobState{}, domain.ErrInvalid
	}
	repository, err := j.maintenanceRepository(ctx)
	if err != nil {
		return domain.ExternalMetadataRemovalJobState{}, err
	}
	return repository.GetExternalMetadataRemovalState(ctx, actor, job)
}

// BackupReminderScheduleRepository queues a background backup check when
// none was queued within interval.
type BackupReminderScheduleRepository interface {
	DispatchBackupReminder(ctx context.Context, days int, interval time.Duration, policy domain.JobPolicy) (bool, error)
}

type backupReminderSchedule struct {
	repository BackupReminderScheduleRepository
	days       int
	now        func() time.Time
	mu         sync.Mutex
	last       time.Time
}

// NewJobsWithBackupReminderSchedule makes the schedule dispatcher also queue
// a background backup check once every domain.BackupReminderInterval with
// threshold days (G13.6). Scan schedules and the consistency check keep
// priority.
func NewJobsWithBackupReminderSchedule(j *Jobs, repository BackupReminderScheduleRepository, days int) (*Jobs, error) {
	if j == nil || j.schedules == nil || repository == nil || !domain.ValidBackupReminderDays(days) {
		return nil, domain.ErrInvalid
	}
	next := *j
	next.backupReminder = &backupReminderSchedule{repository: repository, days: days, now: time.Now}
	return &next, nil
}

func (b *backupReminderSchedule) dispatch(ctx context.Context, policy domain.JobPolicy) (bool, error) {
	b.mu.Lock()
	now := b.now()
	// The same spacing as the consistency dispatch bounds the queries.
	if !b.last.IsZero() && now.Sub(b.last) < consistencyDispatchSpacing {
		b.mu.Unlock()
		return false, nil
	}
	b.last = now
	b.mu.Unlock()
	return b.repository.DispatchBackupReminder(ctx, b.days, domain.BackupReminderInterval, policy)
}
