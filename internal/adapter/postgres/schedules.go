package postgres

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// The library scan schedule of /libraries/{id}/schedule is a view over two
// rows since schema 107: scan_schedules keeps the scan options, the watch and
// its owner; the library's inventory_scan row of job_schedules keeps the
// timing and dispatch state. A definition without a timed row reads as a
// disabled daily interval in UTC.
const scanScheduleSelect = `SELECT s.library_id::text,s.revision,COALESCE(j.enabled,false),COALESCE(j.mode,'interval'),COALESCE(j.interval_seconds,86400),COALESCE(j.cron,''),COALESCE(j.timezone,'UTC'),j.run_at,
 s.probe,s.nfo,s.ignore_mode,s.ignore_case,j.next_due,j.retry_after,COALESCE(j.last_job_id::text,''),COALESCE(j.last_error,''),GREATEST(s.updated_at,COALESCE(j.updated_at,s.updated_at)),s.watch_enabled
 FROM scan_schedules s LEFT JOIN job_schedules j ON j.kind='inventory_scan' AND j.library_id=s.library_id`

func readSchedule(row pgx.Row) (domain.ScanSchedule, error) {
	var v domain.ScanSchedule
	err := row.Scan(&v.LibraryID, &v.Revision, &v.Enabled, &v.Timing.Mode, &v.Timing.IntervalSeconds, &v.Timing.Cron, &v.Timing.Timezone, &v.Timing.RunAt, &v.Probe, &v.NFO, &v.Ignore.Mode, &v.Ignore.CaseMode, &v.NextDue, &v.RetryAfter, &v.LastJobID, &v.LastError, &v.UpdatedAt, &v.Watch)
	return v, storageError(err)
}

func (s *Store) GetScanSchedule(ctx context.Context, a domain.Actor, library string) (domain.ScanSchedule, error) {
	if ctx == nil || !domain.ValidID(library) {
		return domain.ScanSchedule{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.ScanSchedule{}, err
	}
	defer tx.Rollback(ctx)
	v, err := readSchedule(tx.QueryRow(ctx, scanScheduleSelect+` WHERE s.library_id=$1::uuid`, library))
	if err != nil {
		return v, err
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.ScanSchedule{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

func (s *Store) PutScanSchedule(ctx context.Context, a domain.Actor, library string, input domain.ScanScheduleInput, calendar app.ScheduleCalendar) (domain.ScanSchedule, error) {
	if ctx == nil || calendar == nil || !domain.ValidID(library) || input.ExpectedRevision < 0 || input.ExpectedRevision == math.MaxInt64 || domain.ValidateScanIntentWithFamilyIgnore(domain.ScanIntent{Ignore: domain.IgnoreIntent(input.Ignore)}) != nil {
		return domain.ScanSchedule{}, domain.ErrInvalid
	}
	// The library endpoint keeps its interval and cron modes.
	if input.Timing.Mode != domain.ScheduleModeInterval && input.Timing.Mode != domain.ScheduleModeCron || input.Timing.RunAt != nil {
		return domain.ScanSchedule{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.ScanSchedule{}, err
	}
	defer tx.Rollback(ctx)
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return domain.ScanSchedule{}, storageError(err)
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM libraries WHERE id=$1::uuid)`, library).Scan(&exists); err != nil {
		return domain.ScanSchedule{}, storageError(err)
	}
	if !exists {
		return domain.ScanSchedule{}, domain.ErrNotFound
	}
	before, err := readSchedule(tx.QueryRow(ctx, scanScheduleSelect+` WHERE s.library_id=$1::uuid FOR UPDATE OF s`, library))
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.ScanSchedule{}, err
	}
	if before.Revision != input.ExpectedRevision {
		return domain.ScanSchedule{}, domain.ErrConflict
	}
	// A window set through /schedules is kept and still applies.
	var window domain.ScheduleWindow
	if err = tx.QueryRow(ctx, `SELECT window_start,window_end FROM job_schedules WHERE kind='inventory_scan' AND library_id=$1::uuid FOR UPDATE`, library).Scan(&window.Start, &window.End); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.ScanSchedule{}, storageError(err)
	}
	next, _, err := domain.NextScheduleRun(input.Timing, window, now, calendar.Next)
	if err != nil {
		return domain.ScanSchedule{}, err
	}
	var due *time.Time
	if input.Enabled {
		due = &next
	}
	if input.Watch && !before.Watch {
		var enabled int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM scan_schedules WHERE watch_enabled`).Scan(&enabled); err != nil {
			return domain.ScanSchedule{}, storageError(err)
		}
		if enabled >= domain.MaxWatchLibraries {
			return domain.ScanSchedule{}, domain.ErrScanLimit
		}
	}
	// One definition per library bounds storage by the existing library count.
	if _, err = tx.Exec(ctx, `INSERT INTO scan_schedules(library_id,owner_id,revision,probe,nfo,ignore_mode,ignore_case,watch_enabled) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8)
 ON CONFLICT(library_id) DO UPDATE SET owner_id=EXCLUDED.owner_id,revision=EXCLUDED.revision,probe=EXCLUDED.probe,nfo=EXCLUDED.nfo,ignore_mode=EXCLUDED.ignore_mode,ignore_case=EXCLUDED.ignore_case,watch_enabled=EXCLUDED.watch_enabled,updated_at=clock_timestamp()`,
		library, a.UserID, input.ExpectedRevision+1, input.Probe, input.NFO, input.Ignore.Mode, input.Ignore.CaseMode, input.Watch); err != nil {
		return domain.ScanSchedule{}, storageError(err)
	}
	params, err := scheduleParamsJSON(domain.JobInventoryScan, domain.JobScheduleParams{Probe: input.Probe, NFO: input.NFO, Ignore: input.Ignore})
	if err != nil {
		return domain.ScanSchedule{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_schedules(kind,library_id,revision,enabled,mode,interval_seconds,cron,timezone,params,next_due) VALUES('inventory_scan',$1::uuid,1,$2,$3,$4,$5,$6,$7::jsonb,$8)
 ON CONFLICT(kind,COALESCE(library_id,'00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET revision=job_schedules.revision+1,enabled=EXCLUDED.enabled,mode=EXCLUDED.mode,interval_seconds=EXCLUDED.interval_seconds,
 cron=EXCLUDED.cron,run_at=NULL,timezone=EXCLUDED.timezone,params=EXCLUDED.params,next_due=EXCLUDED.next_due,retry_after=NULL,last_error='',updated_at=clock_timestamp()`,
		library, input.Enabled, input.Timing.Mode, input.Timing.IntervalSeconds, input.Timing.Cron, input.Timing.Timezone, params, due); err != nil {
		return domain.ScanSchedule{}, storageError(err)
	}
	if input.Watch {
		if _, err = tx.Exec(ctx, `INSERT INTO scan_watch_state(library_id) VALUES($1::uuid) ON CONFLICT(library_id) DO UPDATE SET observe_after=NULL,retry_after=NULL`, library); err != nil {
			return domain.ScanSchedule{}, storageError(err)
		}
	}
	v, err := readSchedule(tx.QueryRow(ctx, scanScheduleSelect+` WHERE s.library_id=$1::uuid`, library))
	if err != nil {
		return v, err
	}
	if err = auditAccount(ctx, tx, a, "schedule.updated", library, before, v); err != nil {
		return domain.ScanSchedule{}, err
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.ScanSchedule{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

func scheduleRetryable(err error) bool {
	for _, target := range []error{domain.ErrConflict, domain.ErrJobBusy, domain.ErrJobQueueFull, domain.ErrScanUnavailable, domain.ErrScanLimit, domain.ErrProbeDisabled, domain.ErrNFODisabled, domain.ErrNFOReaderUnavailable, domain.ErrIgnoreUnavailable} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
