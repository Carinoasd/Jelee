package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

const jobScheduleColumns = `id::text,kind,COALESCE(library_id::text,''),revision,enabled,mode,interval_seconds,cron,timezone,run_at,window_start,window_end,params::text,next_due,retry_after,COALESCE(last_job_id::text,''),last_error,last_run_at,created_at,updated_at`

// jobScheduleLimit bounds a listing: one row per kind and library.
const jobScheduleLimit = 1000

func readJobSchedule(row pgx.Row) (domain.JobSchedule, error) {
	var v domain.JobSchedule
	var params string
	err := row.Scan(&v.ID, &v.Kind, &v.LibraryID, &v.Revision, &v.Enabled, &v.Timing.Mode, &v.Timing.IntervalSeconds, &v.Timing.Cron, &v.Timing.Timezone, &v.Timing.RunAt, &v.Window.Start, &v.Window.End, &params, &v.NextDue, &v.RetryAfter, &v.LastJobID, &v.LastError, &v.LastRunAt, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, storageError(err)
	}
	if err = json.Unmarshal([]byte(params), &v.Params); err != nil {
		return v, domain.ErrDatabase
	}
	for _, t := range []*time.Time{v.Timing.RunAt, v.NextDue, v.RetryAfter, v.LastRunAt} {
		if t != nil {
			*t = t.UTC()
		}
	}
	v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
	return v, nil
}

// scheduleParamsJSON stores only the options of kind, so a scan schedule's
// params equal the scan_schedules options exactly.
func scheduleParamsJSON(kind string, p domain.JobScheduleParams) (string, error) {
	var value any = map[string]any{}
	switch kind {
	case domain.JobInventoryScan:
		value = map[string]any{"probe": p.Probe, "nfo": p.NFO, "ignore": map[string]any{"mode": p.Ignore.Mode, "caseMode": p.Ignore.CaseMode}}
	case domain.JobSubtitleCacheCleanup:
		value = map[string]any{"maxIdleDays": p.MaxIdleDays}
	case domain.JobBackupReminder:
		value = map[string]any{"thresholdDays": p.ThresholdDays}
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", domain.ErrInvalid
	}
	return string(b), nil
}

// ListJobSchedules lists every schedule, or a library's, in kind order.
func (s *Store) ListJobSchedules(ctx context.Context, a domain.Actor, library string) ([]domain.JobSchedule, error) {
	if ctx == nil || library != "" && !domain.ValidID(library) {
		return nil, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+jobScheduleColumns+` FROM job_schedules WHERE ($1='' OR library_id=NULLIF($1,'')::uuid) ORDER BY library_id NULLS FIRST,kind,id LIMIT $2`, library, jobScheduleLimit)
	if err != nil {
		return nil, storageError(err)
	}
	out := []domain.JobSchedule{}
	for rows.Next() {
		v, err := readJobSchedule(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return nil, err
	}
	return out, storageError(tx.Commit(ctx))
}

// GetJobSchedule reads one schedule for an administrator.
func (s *Store) GetJobSchedule(ctx context.Context, a domain.Actor, id string) (domain.JobSchedule, error) {
	if ctx == nil {
		return domain.JobSchedule{}, domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return domain.JobSchedule{}, domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.JobSchedule{}, err
	}
	defer tx.Rollback(ctx)
	v, err := readJobSchedule(tx.QueryRow(ctx, `SELECT `+jobScheduleColumns+` FROM job_schedules WHERE id=$1::uuid`, id))
	if err != nil {
		return v, err
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.JobSchedule{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

// PutJobSchedule creates a schedule (id empty, expected revision 0) or
// replaces one under its revision. A second schedule of the same kind and
// scope is domain.ErrConflict. A scan schedule also writes its options to the
// library's scan definition, which the directory watch runs with.
func (s *Store) PutJobSchedule(ctx context.Context, a domain.Actor, id string, input domain.JobScheduleInput, calendar app.ScheduleCalendar) (domain.JobSchedule, error) {
	if ctx == nil || calendar == nil || id != "" && !domain.ValidID(id) {
		return domain.JobSchedule{}, domain.ErrInvalid
	}
	if err := domain.ValidateJobScheduleInput(input); err != nil {
		return domain.JobSchedule{}, err
	}
	if id == "" && input.ExpectedRevision != 0 {
		return domain.JobSchedule{}, domain.ErrConflict
	}
	params, err := scheduleParamsJSON(input.Kind, input.Params)
	if err != nil {
		return domain.JobSchedule{}, err
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.JobSchedule{}, err
	}
	defer tx.Rollback(ctx)
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return domain.JobSchedule{}, storageError(err)
	}
	next, more, err := domain.NextScheduleRun(input.Timing, input.Window, now, calendar.Next)
	if err != nil {
		return domain.JobSchedule{}, err
	}
	// A once schedule whose time already passed is stored disabled.
	var due *time.Time
	if input.Enabled && more {
		due = &next
	}
	if input.LibraryID != "" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM libraries WHERE id=$1::uuid)`, input.LibraryID).Scan(&exists); err != nil {
			return domain.JobSchedule{}, storageError(err)
		}
		if !exists {
			return domain.JobSchedule{}, domain.ErrNotFound
		}
	}
	var before *domain.JobSchedule
	if id != "" {
		old, err := readJobSchedule(tx.QueryRow(ctx, `SELECT `+jobScheduleColumns+` FROM job_schedules WHERE id=$1::uuid FOR UPDATE`, id))
		if err != nil {
			return domain.JobSchedule{}, err
		}
		if old.Kind != input.Kind || old.LibraryID != input.LibraryID {
			return domain.JobSchedule{}, domain.ErrInvalid
		}
		if old.Revision != input.ExpectedRevision {
			return domain.JobSchedule{}, domain.ErrConflict
		}
		before = &old
	}
	var v domain.JobSchedule
	if before == nil {
		v, err = readJobSchedule(tx.QueryRow(ctx, `INSERT INTO job_schedules(kind,library_id,revision,enabled,mode,interval_seconds,cron,run_at,timezone,window_start,window_end,params,next_due)
 VALUES($1,NULLIF($2,'')::uuid,1,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12) ON CONFLICT DO NOTHING RETURNING `+jobScheduleColumns,
			input.Kind, input.LibraryID, input.Enabled && more, input.Timing.Mode, input.Timing.IntervalSeconds, input.Timing.Cron, input.Timing.RunAt, input.Timing.Timezone, input.Window.Start, input.Window.End, params, due))
		if errors.Is(err, domain.ErrNotFound) {
			return domain.JobSchedule{}, domain.ErrConflict
		}
	} else {
		v, err = readJobSchedule(tx.QueryRow(ctx, `UPDATE job_schedules SET revision=revision+1,enabled=$2,mode=$3,interval_seconds=$4,cron=$5,run_at=$6,timezone=$7,window_start=$8,window_end=$9,params=$10::jsonb,next_due=$11,
 retry_after=NULL,last_error='',updated_at=clock_timestamp() WHERE id=$1::uuid RETURNING `+jobScheduleColumns,
			id, input.Enabled && more, input.Timing.Mode, input.Timing.IntervalSeconds, input.Timing.Cron, input.Timing.RunAt, input.Timing.Timezone, input.Window.Start, input.Window.End, params, due))
	}
	if err != nil {
		return domain.JobSchedule{}, err
	}
	if v.Kind == domain.JobInventoryScan {
		// The scan definition keeps the same options; a changed definition
		// gets a new revision so a library schedule client sees the change.
		if _, err = tx.Exec(ctx, `INSERT INTO scan_schedules(library_id,owner_id,revision,probe,nfo,ignore_mode,ignore_case) VALUES($1::uuid,$2::uuid,1,$3,$4,$5,$6)
 ON CONFLICT(library_id) DO UPDATE SET revision=scan_schedules.revision+1,probe=EXCLUDED.probe,nfo=EXCLUDED.nfo,ignore_mode=EXCLUDED.ignore_mode,ignore_case=EXCLUDED.ignore_case,updated_at=clock_timestamp()
 WHERE (scan_schedules.probe,scan_schedules.nfo,scan_schedules.ignore_mode,scan_schedules.ignore_case) IS DISTINCT FROM (EXCLUDED.probe,EXCLUDED.nfo,EXCLUDED.ignore_mode,EXCLUDED.ignore_case)`,
			v.LibraryID, a.UserID, v.Params.Probe, v.Params.NFO, v.Params.Ignore.Mode, v.Params.Ignore.CaseMode); err != nil {
			return domain.JobSchedule{}, storageError(err)
		}
	}
	event := "schedule.created"
	var previous any
	if before != nil {
		event, previous = "schedule.updated", *before
	}
	if err = auditAccount(ctx, tx, a, event, v.ID, previous, v); err != nil {
		return domain.JobSchedule{}, err
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.JobSchedule{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

// DeleteJobSchedule removes a schedule under its revision. A library scan
// definition and its watch stay.
func (s *Store) DeleteJobSchedule(ctx context.Context, a domain.Actor, id string, expectedRevision int64) error {
	if ctx == nil || expectedRevision < 1 {
		return domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	before, err := readJobSchedule(tx.QueryRow(ctx, `SELECT `+jobScheduleColumns+` FROM job_schedules WHERE id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if before.Revision != expectedRevision {
		return domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `DELETE FROM job_schedules WHERE id=$1::uuid`, id); err != nil {
		return storageError(err)
	}
	if err = auditAccount(ctx, tx, a, "schedule.deleted", id, before, nil); err != nil {
		return err
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// errScheduleKindUnavailable marks an occurrence this server cannot run.
var errScheduleKindUnavailable = errors.New("scheduled kind unavailable on this server")

// DispatchJobSchedule elects the dispatcher of one due occurrence in a short
// transaction holding the account and jobs locks, in the order public
// admission takes them. The job is queued as the system actor of the
// schedule: no user session or administrator is needed. An occurrence outside
// the schedule's window moves to the window's next opening without a job; a
// busy library or full queue keeps it for a retry; a kind this server cannot
// run skips it. No lease survives the transaction.
func (s *Store) DispatchJobSchedule(parent context.Context, p domain.JobPolicy, calendar app.ScheduleCalendar, caps app.ScheduleCapabilities) (bool, error) {
	if parent == nil || calendar == nil || !validJobPolicy(p) {
		return false, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.accountTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = lockJobs(ctx, tx); err != nil {
		return false, err
	}
	v, err := readJobSchedule(tx.QueryRow(ctx, `SELECT `+jobScheduleColumns+` FROM job_schedules WHERE enabled AND next_due<=statement_timestamp() AND (retry_after IS NULL OR retry_after<=statement_timestamp()) ORDER BY next_due,id LIMIT 1 FOR UPDATE`))
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return false, storageError(err)
	}
	zone, err := time.LoadLocation(v.Timing.Timezone)
	if err != nil {
		return false, domain.ErrInvalid
	}
	if !v.Window.Allows(now, zone) {
		opening, ok := v.Window.NextAllowed(now, zone)
		if !ok {
			return false, domain.ErrInvalid
		}
		_, err = tx.Exec(ctx, `UPDATE job_schedules SET next_due=$2,retry_after=NULL,updated_at=clock_timestamp() WHERE id=$1::uuid`, v.ID, opening)
		if err != nil {
			return false, storageError(err)
		}
		return true, storageError(tx.Commit(ctx))
	}
	next, more, err := domain.NextScheduleRun(v.Timing, v.Window, now, calendar.Next)
	if err != nil {
		return false, err
	}
	var due *time.Time
	if more {
		due = &next
	}
	// Admission side effects roll back to this savepoint when the occurrence
	// cannot be admitted now.
	work, err := tx.Begin(ctx)
	if err != nil {
		return false, storageError(err)
	}
	key := fmt.Sprintf("schedule:%s:%d:%d", v.ID, v.Revision, v.NextDue.UnixMicro())
	job, admissionErr := s.admitScheduledJob(ctx, work, v, key, p, caps)
	switch {
	case admissionErr == nil:
		if err = work.Commit(ctx); err != nil {
			return false, storageError(err)
		}
		_, err = tx.Exec(ctx, `UPDATE job_schedules SET enabled=$2 AND enabled,next_due=CASE WHEN $2 THEN $3::timestamptz END,retry_after=NULL,last_job_id=$4::uuid,last_error='',last_run_at=$5,updated_at=clock_timestamp() WHERE id=$1::uuid`, v.ID, more, due, job.ID, now)
	case errors.Is(admissionErr, errScheduleKindUnavailable):
		if err = work.Rollback(ctx); err != nil {
			return false, storageError(err)
		}
		_, err = tx.Exec(ctx, `UPDATE job_schedules SET enabled=$2 AND enabled,next_due=CASE WHEN $2 THEN $3::timestamptz END,retry_after=NULL,last_error='kind_unavailable',updated_at=clock_timestamp() WHERE id=$1::uuid`, v.ID, more, due)
	case scheduleRetryable(admissionErr):
		if err = work.Rollback(ctx); err != nil {
			return false, storageError(err)
		}
		_, err = tx.Exec(ctx, `UPDATE job_schedules SET retry_after=clock_timestamp()+$2*interval '1 microsecond',last_error='admission_unavailable',updated_at=clock_timestamp() WHERE id=$1::uuid`, v.ID, domain.ScheduleRetryDelay.Microseconds())
	default:
		_ = work.Rollback(ctx)
		return false, admissionErr
	}
	if err != nil {
		return false, storageError(err)
	}
	return true, storageError(tx.Commit(ctx))
}

// admitScheduledJob queues the schedule's job inside tx as the system actor
// and records the schedule as its source.
func (s *Store) admitScheduledJob(ctx context.Context, tx pgx.Tx, v domain.JobSchedule, key string, p domain.JobPolicy, caps app.ScheduleCapabilities) (domain.Job, error) {
	actor := domain.SystemActor(domain.SystemTriggerSchedule, v.ID)
	priority := domain.JobPriorityBackground
	var job domain.Job
	var err error
	switch v.Kind {
	case domain.JobInventoryScan:
		intent := domain.ScanIntent{NFO: v.Params.NFO, Ignore: domain.IgnoreIntent(v.Params.Ignore)}
		if v.Params.Probe {
			intent.Probe.Scope = domain.ProbeScopeIncremental
		}
		job, _, err = s.submitScanInTransaction(ctx, tx, actor, v.LibraryID, "", key, priority, intent, p, caps.Probe, caps.NFO, caps.Ignore, true, func() error { return nil })
		if err != nil {
			return domain.Job{}, err
		}
		return job, markSystemJob(ctx, tx, job.ID, actor)
	case domain.JobMetadataRefresh:
		if !caps.MetadataRefresh {
			return domain.Job{}, errScheduleKindUnavailable
		}
		if err = libraryAdmission(ctx, tx, v.LibraryID, p); err != nil {
			return domain.Job{}, err
		}
		if job, err = insertMetadataRefreshJob(ctx, tx, v.LibraryID, nil, key, priority, domain.MetadataRefreshTriggerSchedule, p); err != nil {
			return domain.Job{}, err
		}
		err = auditAccount(ctx, tx, actor, "metadata_refresh.submitted", job.ID, nil, map[string]any{"libraryId": v.LibraryID, "priority": priority, "trigger": domain.MetadataRefreshTriggerSchedule, "scheduleId": v.ID})
	case domain.JobImageRefresh:
		if !caps.ImageRefresh {
			return domain.Job{}, errScheduleKindUnavailable
		}
		if err = libraryAdmission(ctx, tx, v.LibraryID, p); err != nil {
			return domain.Job{}, err
		}
		if job, err = insertImageRefreshJob(ctx, tx, v.LibraryID, nil, key, priority, p); err != nil {
			return domain.Job{}, err
		}
		err = auditAccount(ctx, tx, actor, "image.refresh_submitted", job.ID, nil, map[string]any{"libraryId": v.LibraryID, "priority": priority, "scheduleId": v.ID})
	case domain.JobConsistencyCheck:
		if err = libraryAdmission(ctx, tx, v.LibraryID, p); err != nil {
			return domain.Job{}, err
		}
		job, err = insertConsistencyJob(ctx, tx, v.LibraryID, priority, p)
	case domain.JobSubtitleCacheCleanup:
		if !caps.SubtitleCache {
			return domain.Job{}, errScheduleKindUnavailable
		}
		days := v.Params.MaxIdleDays
		if days == 0 {
			days = domain.SubtitleCacheIdleDaysDefault
		}
		var library string
		if library, err = anchorInstanceJob(ctx, tx, domain.JobSubtitleCacheCleanup, p); err != nil {
			return domain.Job{}, err
		}
		if job, err = insertKindJob(ctx, tx, library, nil, key, domain.JobSubtitleCacheCleanup, priority, p, nil); err != nil {
			return domain.Job{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO subtitle_cache_cleanup_job_state(job_id,max_idle_days,updated_at) VALUES($1::uuid,$2,clock_timestamp())`, job.ID, days); err != nil {
			return domain.Job{}, storageError(err)
		}
		err = auditAccount(ctx, tx, actor, "subtitle_cache_cleanup.submitted", job.ID, nil, map[string]any{"anchorLibraryId": library, "priority": priority, "maxIdleDays": days, "scheduleId": v.ID})
	case domain.JobBackupReminder:
		days := v.Params.ThresholdDays
		if days == 0 {
			days = caps.BackupReminderDays
		}
		if days == 0 {
			days = domain.BackupReminderDaysDefault
		}
		var library string
		if library, err = anchorInstanceJob(ctx, tx, domain.JobBackupReminder, p); err != nil {
			return domain.Job{}, err
		}
		if job, err = insertBackupReminder(ctx, tx, library, nil, key, priority, domain.BackupReminderSchedule, days, p); err != nil {
			return domain.Job{}, err
		}
		err = auditAccount(ctx, tx, actor, "backup_reminder.submitted", job.ID, nil, map[string]any{"anchorLibraryId": library, "priority": priority, "thresholdDays": days, "scheduleId": v.ID})
	default:
		return domain.Job{}, domain.ErrScheduleKindNotAllowed
	}
	if err != nil {
		return domain.Job{}, err
	}
	if err = trimJobs(ctx, tx, p.HistoryLimit); err != nil {
		return domain.Job{}, err
	}
	return job, markSystemJob(ctx, tx, job.ID, actor)
}

// markSystemJob records the system actor's source on a job queued without a
// user; the jobs CHECK refuses it on a job with a user or of a kind the
// system actor may not run.
func markSystemJob(ctx context.Context, tx pgx.Tx, job string, actor domain.Actor) error {
	if !actor.IsSystem() {
		return domain.ErrInvalid
	}
	tag, err := tx.Exec(ctx, `UPDATE jobs SET system_source=$2 WHERE id=$1::uuid AND actor_id IS NULL`, job, actor.System)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return nil
}

// jobSystemActor is the system actor a job was queued under, if any.
func jobSystemActor(ctx context.Context, tx pgx.Tx, job string) (domain.Actor, bool, error) {
	var source *string
	if err := tx.QueryRow(ctx, `SELECT system_source FROM jobs WHERE id=$1::uuid`, job).Scan(&source); err != nil {
		return domain.Actor{}, false, storageError(err)
	}
	if source == nil {
		return domain.Actor{}, false, nil
	}
	actor := domain.Actor{System: *source}
	if !actor.IsSystem() {
		return domain.Actor{}, false, domain.ErrDatabase
	}
	return actor, true, nil
}
