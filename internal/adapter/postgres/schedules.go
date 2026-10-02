package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"time"
)

const scheduleColumns = `library_id::text,revision,enabled,mode,interval_seconds,cron,timezone,probe,nfo,ignore_mode,ignore_case,next_due,retry_after,COALESCE(last_job_id::text,''),last_error,updated_at,watch_enabled`

func readSchedule(row pgx.Row) (domain.ScanSchedule, error) {
	var v domain.ScanSchedule
	err := row.Scan(&v.LibraryID, &v.Revision, &v.Enabled, &v.Timing.Mode, &v.Timing.IntervalSeconds, &v.Timing.Cron, &v.Timing.Timezone, &v.Probe, &v.NFO, &v.Ignore.Mode, &v.Ignore.CaseMode, &v.NextDue, &v.RetryAfter, &v.LastJobID, &v.LastError, &v.UpdatedAt, &v.Watch)
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
	v, err := readSchedule(tx.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM scan_schedules WHERE library_id=$1::uuid`, library))
	if err != nil {
		return v, err
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.ScanSchedule{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

func (s *Store) PutScanSchedule(ctx context.Context, a domain.Actor, library string, input domain.ScanScheduleInput, calendar app.ScheduleCalendar) (domain.ScanSchedule, error) {
	if ctx == nil || calendar == nil || !domain.ValidID(library) || input.ExpectedRevision < 0 || input.ExpectedRevision >= 9223372036854775807 || domain.ValidateScanIntentWithFamilyIgnore(domain.ScanIntent{Ignore: domain.IgnoreIntent(input.Ignore)}) != nil {
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
	next, err := calendar.Next(input.Timing, now)
	if err != nil {
		return domain.ScanSchedule{}, err
	}
	var due *time.Time
	if input.Enabled {
		due = &next
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM libraries WHERE id=$1::uuid)`, library).Scan(&exists); err != nil {
		return domain.ScanSchedule{}, storageError(err)
	}
	if !exists {
		return domain.ScanSchedule{}, domain.ErrNotFound
	}
	before, err := readSchedule(tx.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM scan_schedules WHERE library_id=$1::uuid FOR UPDATE`, library))
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.ScanSchedule{}, err
	}
	if before.Revision != input.ExpectedRevision {
		return domain.ScanSchedule{}, domain.ErrConflict
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
	v, err := readSchedule(tx.QueryRow(ctx, `INSERT INTO scan_schedules(library_id,owner_id,revision,enabled,mode,interval_seconds,cron,timezone,probe,nfo,ignore_mode,ignore_case,next_due,watch_enabled) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
 ON CONFLICT(library_id) DO UPDATE SET owner_id=EXCLUDED.owner_id,revision=EXCLUDED.revision,enabled=EXCLUDED.enabled,mode=EXCLUDED.mode,interval_seconds=EXCLUDED.interval_seconds,cron=EXCLUDED.cron,timezone=EXCLUDED.timezone,probe=EXCLUDED.probe,nfo=EXCLUDED.nfo,ignore_mode=EXCLUDED.ignore_mode,ignore_case=EXCLUDED.ignore_case,next_due=EXCLUDED.next_due,watch_enabled=EXCLUDED.watch_enabled,retry_after=NULL,last_error='',updated_at=clock_timestamp() RETURNING `+scheduleColumns, library, a.UserID, input.ExpectedRevision+1, input.Enabled, input.Timing.Mode, input.Timing.IntervalSeconds, input.Timing.Cron, input.Timing.Timezone, input.Probe, input.NFO, input.Ignore.Mode, input.Ignore.CaseMode, due, input.Watch))
	if err != nil {
		return v, err
	}
	if input.Watch {
		if _, err = tx.Exec(ctx, `INSERT INTO scan_watch_state(library_id) VALUES($1::uuid) ON CONFLICT(library_id) DO UPDATE SET observe_after=NULL,retry_after=NULL`, library); err != nil {
			return domain.ScanSchedule{}, storageError(err)
		}
	}
	if err = auditAccount(ctx, tx, a, "schedule.updated", library, before, v); err != nil {
		return domain.ScanSchedule{}, err
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.ScanSchedule{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

func scheduleOwnerLive(ctx context.Context, tx pgx.Tx, owner string) error {
	var live bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1::uuid AND is_admin AND NOT disabled AND deleted_at IS NULL)`, owner).Scan(&live)
	if err != nil {
		return storageError(err)
	}
	if !live {
		return domain.ErrForbidden
	}
	return nil
}

// A short transaction elects the dispatcher for a due occurrence. Account and
// jobs locks follow the same order as public admission. No lease survives the
// transaction, so a crashed process cannot retain leadership or publish late.
func (s *Store) DispatchScanSchedule(parent context.Context, p domain.JobPolicy, calendar app.ScheduleCalendar, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity, caps app.IgnoreAdmissionCapabilities) (bool, error) {
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
	v, err := readSchedule(tx.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM scan_schedules WHERE enabled AND next_due<=statement_timestamp() AND (retry_after IS NULL OR retry_after<=statement_timestamp()) ORDER BY next_due,library_id LIMIT 1 FOR UPDATE`))
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var owner string
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT owner_id::text,clock_timestamp() FROM scan_schedules WHERE library_id=$1::uuid`, v.LibraryID).Scan(&owner, &now); err != nil {
		return false, storageError(err)
	}
	guard := func() error { return scheduleOwnerLive(ctx, tx, owner) }
	if err = guard(); err != nil {
		if !errors.Is(err, domain.ErrForbidden) {
			return false, err
		}
		_, err = tx.Exec(ctx, `UPDATE scan_schedules SET enabled=false,next_due=NULL,retry_after=NULL,last_error='owner_unavailable',updated_at=clock_timestamp() WHERE library_id=$1::uuid`, v.LibraryID)
		if err != nil {
			return false, storageError(err)
		}
		return true, storageError(tx.Commit(ctx))
	}
	next, err := calendar.Next(v.Timing, now)
	if err != nil {
		return false, err
	}
	intent := domain.ScanIntent{NFO: v.NFO, Ignore: domain.IgnoreIntent(v.Ignore)}
	if v.Probe {
		intent.Probe.Scope = domain.ProbeScopeIncremental
	}
	// Roll back admission side effects if capability/capacity is temporarily
	// unavailable, while retaining a bounded retry delay for this due occurrence.
	work, err := tx.Begin(ctx)
	if err != nil {
		return false, storageError(err)
	}
	key := fmt.Sprintf("schedule:%s:%d:%d", v.LibraryID, v.Revision, v.NextDue.UnixMicro())
	job, _, admissionErr := s.submitScanInTransaction(ctx, work, domain.Actor{UserID: owner}, v.LibraryID, "", key, domain.JobPriorityBackground, intent, p, probe, nfo, caps, true, guard)
	if admissionErr != nil {
		if err = work.Rollback(ctx); err != nil {
			return false, storageError(err)
		}
		if !scheduleRetryable(admissionErr) {
			return false, admissionErr
		}
		_, err = tx.Exec(ctx, `UPDATE scan_schedules SET retry_after=clock_timestamp()+interval '30 seconds',last_error='admission_unavailable',updated_at=clock_timestamp() WHERE library_id=$1::uuid`, v.LibraryID)
	} else {
		if err = work.Commit(ctx); err != nil {
			return false, storageError(err)
		}
		_, err = tx.Exec(ctx, `UPDATE scan_schedules SET next_due=$2,retry_after=NULL,last_job_id=$3::uuid,last_error='',updated_at=clock_timestamp() WHERE library_id=$1::uuid`, v.LibraryID, next, job.ID)
	}
	if err != nil {
		return false, storageError(err)
	}
	if err = guard(); err != nil {
		return false, err
	}
	return true, storageError(tx.Commit(ctx))
}

func scheduleRetryable(err error) bool {
	for _, target := range []error{domain.ErrConflict, domain.ErrJobBusy, domain.ErrJobQueueFull, domain.ErrScanUnavailable, domain.ErrScanLimit, domain.ErrProbeDisabled, domain.ErrNFODisabled, domain.ErrNFOReaderUnavailable, domain.ErrIgnoreUnavailable} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
