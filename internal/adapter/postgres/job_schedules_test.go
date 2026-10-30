package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/calendar"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func dispatchJobSchedules(f jobFixture, caps app.ScheduleCapabilities) (bool, error) {
	return f.s.DispatchJobSchedule(f.ctx, f.policy, calendar.Calendar{}, caps)
}

func dueJobSchedule(t *testing.T, f jobFixture, id string) {
	t.Helper()
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_schedules SET next_due=clock_timestamp()-interval '1 minute',retry_after=NULL WHERE id=$1::uuid AND enabled`, id); err != nil {
		t.Fatal(err)
	}
}

func finishActiveJobs(t *testing.T, f jobFixture) {
	t.Helper()
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='succeeded',finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE state IN ('queued','running')`); err != nil {
		t.Fatal(err)
	}
}

// waitTerminalJob polls the job row directly: the tests that use it have no
// administrator session left to read it through the store.
func waitTerminalJob(t *testing.T, f jobFixture, id string) (string, string) {
	t.Helper()
	for range 600 {
		var state, code string
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT state,error_code FROM jobs WHERE id=$1::uuid`, id).Scan(&state, &code); err != nil {
			t.Fatal(err)
		}
		if state != domain.JobQueued && state != domain.JobRunning {
			return state, code
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("job did not finish", id)
	return "", ""
}

// clockAfter renders the UTC wall clock d from now as HH:MM.
func clockAfter(d time.Duration) string {
	return time.Now().UTC().Add(d).Format("15:04")
}

func scheduleCount(t *testing.T, f jobFixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(query, err)
	}
	return n
}

// Library scan schedules written by schema 105 move into job_schedules with
// the same timing, zone, due time and options; the library endpoint reads
// them back unchanged; the downgrade restores them; a due one is dispatched
// as the system actor.
func TestJobScheduleMigratesScanSchedules(t *testing.T) {
	f := newJobFixtureWithin(t, 3*time.Minute)
	other, err := f.s.RegisterLibrary(f.ctx, "schedule-watch-only", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	version := downgradeAboveMigration(t, f, "job_schedules")
	dsn := f.s.Pool.Config().ConnString()
	if previous, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || previous >= version {
		t.Fatalf("downgrade: version=%d dirty=%t error=%v", previous, dirty, err)
	}
	legacy := func() {
		t.Helper()
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO scan_schedules(library_id,owner_id,revision,enabled,mode,interval_seconds,cron,timezone,probe,nfo,ignore_mode,ignore_case,next_due,last_error,watch_enabled)
 VALUES($1::uuid,$3::uuid,3,true,'cron',0,'30 3 * * *','Asia/Taipei',false,false,'','','2026-01-02T19:30:00Z','admission_unavailable',false),
 ($2::uuid,$3::uuid,7,false,'interval',7200,'','Europe/Berlin',true,true,'jeleeignore','sensitive',NULL,'owner_unavailable',true)`, f.registration.Library.ID, other.Library.ID, f.a.UserID); err != nil {
			t.Fatal(err)
		}
	}
	legacy()
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO scan_watch_state(library_id) VALUES($1::uuid)`, other.Library.ID); err != nil {
		t.Fatal(err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || v != SchemaVersion {
		t.Fatalf("upgrade: version=%d dirty=%t error=%v", v, dirty, err)
	}
	check := func(stage string) {
		t.Helper()
		v, err := f.s.GetScanSchedule(f.ctx, f.a, f.registration.Library.ID)
		due := time.Date(2026, 1, 2, 19, 30, 0, 0, time.UTC)
		if err != nil || v.Revision != 3 || !v.Enabled || v.Timing != (domain.ScheduleTiming{Mode: "cron", Cron: "30 3 * * *", Timezone: "Asia/Taipei"}) || v.NFO || v.Probe ||
			v.Ignore != (domain.ScheduleIgnore{}) || v.NextDue == nil || !v.NextDue.Equal(due) || v.LastError != "admission_unavailable" || v.Watch {
			t.Fatalf("%s: cron schedule changed: %+v %v", stage, v, err)
		}
		w, err := f.s.GetScanSchedule(f.ctx, f.a, other.Library.ID)
		if err != nil || w.Revision != 7 || w.Enabled || w.Timing != (domain.ScheduleTiming{Mode: "interval", IntervalSeconds: 7200, Timezone: "Europe/Berlin"}) || !w.Probe || !w.NFO || w.Ignore != (domain.ScheduleIgnore{Mode: "jeleeignore", CaseMode: "sensitive"}) || w.NextDue != nil || w.LastError != "owner_unavailable" || !w.Watch {
			t.Fatalf("%s: watched schedule changed: %+v %v", stage, w, err)
		}
	}
	check("upgrade")
	if scheduleCount(t, f, `SELECT count(*) FROM job_schedules WHERE kind='inventory_scan' AND params=jsonb_build_object('probe',true,'nfo',true,'ignore',jsonb_build_object('mode','jeleeignore','caseMode','sensitive'))`) != 1 {
		t.Fatal("scan options not carried into params")
	}
	// Round trip: the downgrade writes the timing back to scan_schedules.
	if previous, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || previous >= version {
		t.Fatalf("round trip down: version=%d dirty=%t error=%v", previous, dirty, err)
	}
	if scheduleCount(t, f, `SELECT count(*) FROM scan_schedules WHERE (library_id=$1::uuid AND revision=3 AND enabled AND mode='cron' AND cron='30 3 * * *' AND timezone='Asia/Taipei' AND next_due='2026-01-02T19:30:00Z' AND last_error='admission_unavailable')
 OR (library_id=$2::uuid AND revision=7 AND NOT enabled AND mode='interval' AND interval_seconds=7200 AND timezone='Europe/Berlin' AND next_due IS NULL AND last_error='owner_unavailable' AND watch_enabled)`, f.registration.Library.ID, other.Library.ID) != 2 {
		t.Fatal("downgrade did not restore the scan schedules")
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || v != SchemaVersion {
		t.Fatalf("round trip up: version=%d dirty=%t error=%v", v, dirty, err)
	}
	check("round trip")

	// Behaviour is unchanged except for the identity: a due occurrence
	// queues one background scan, now as the system actor, even though the
	// account that saved it is disabled and has no session.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID); err != nil {
		t.Fatal(err)
	}
	if worked, err := dispatchJobSchedules(f, app.ScheduleCapabilities{}); err != nil || !worked {
		t.Fatal("dispatch", worked, err)
	}
	var id, job, source, priority string
	var next time.Time
	var actor *string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT s.id::text,j.id::text,j.system_source,j.priority,j.actor_id::text,s.next_due FROM job_schedules s JOIN jobs j ON j.id=s.last_job_id WHERE s.library_id=$1::uuid`, f.registration.Library.ID).Scan(&id, &job, &source, &priority, &actor, &next); err != nil {
		t.Fatal(err)
	}
	if source != "system:schedule:"+id || priority != domain.JobPriorityBackground || actor != nil || !next.After(time.Now()) {
		t.Fatal("scheduled scan", source, priority, actor, next)
	}
	// The next occurrence is the cron in its own zone: 03:30 in Taipei.
	if local := next.In(time.FixedZone("CST", 8*3600)); local.Hour() != 3 || local.Minute() != 30 {
		t.Fatal("cron zone lost", local)
	}
	var request string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT request_id FROM audit_logs WHERE event='job.submitted' AND target_id=$1::uuid AND actor_id IS NULL`, job).Scan(&request); err != nil || request != source {
		t.Fatal("scheduled scan not audited as the system", request, err)
	}
	if worked, err := dispatchJobSchedules(f, app.ScheduleCapabilities{}); err != nil || worked {
		t.Fatal("disabled or future schedules dispatched", worked, err)
	}
	// A system job is retained state an older binary cannot attribute.
	refuseRetainedDowngrade(t, f, "job_schedules", "system job silently downgraded")
}

func TestJobScheduleKindsWindowsAndRefusals(t *testing.T) {
	f := newJobFixtureWithin(t, 3*time.Minute)
	library := f.registration.Library.ID
	cal := calendar.Calendar{}
	hourly := domain.ScheduleTiming{Mode: domain.ScheduleModeInterval, IntervalSeconds: 3600, Timezone: "UTC"}
	// Destructive and prepared kinds cannot be scheduled, by the store and
	// by the table itself.
	for _, kind := range []string{domain.JobExternalMetadataRemoval, domain.JobNFOApply, "nfo_write", "catalog_sync", "unknown"} {
		if _, err := f.s.PutJobSchedule(f.ctx, f.a, "", domain.JobScheduleInput{Kind: kind, LibraryID: library, Enabled: true, Timing: hourly}, cal); !errors.Is(err, domain.ErrScheduleKindNotAllowed) {
			t.Fatal("kind accepted", kind, err)
		}
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_schedules(kind,library_id,revision,mode,interval_seconds,cron,timezone) VALUES('external_metadata_removal',$1::uuid,1,'interval',3600,'','UTC')`, library); itemImageSQLState(err) != "23514" {
		t.Fatal("table accepted a destructive kind", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO jobs(library_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,system_source)
 VALUES($1::uuid,'forged','external_metadata_removal','background',100,20,100000,10000,3,100,20,'system:schedule:'||gen_random_uuid())`, library); itemImageSQLState(err) != "23514" {
		t.Fatal("system actor accepted for a destructive job", err)
	}
	for _, bad := range []domain.JobScheduleInput{
		{Kind: domain.JobBackupReminder, LibraryID: library, Enabled: true, Timing: hourly},
		{Kind: domain.JobImageRefresh, Enabled: true, Timing: hourly},
		{Kind: domain.JobImageRefresh, LibraryID: library, Enabled: true, Timing: hourly, Params: domain.JobScheduleParams{MaxIdleDays: 3}},
		{Kind: domain.JobImageRefresh, LibraryID: library, Enabled: true, Timing: hourly, Window: domain.ScheduleWindow{Start: "02:00"}},
	} {
		if _, err := f.s.PutJobSchedule(f.ctx, f.a, "", bad, cal); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("malformed schedule accepted", bad, err)
		}
	}
	if _, err := f.s.PutJobSchedule(f.ctx, domain.Actor{}, "", domain.JobScheduleInput{Kind: domain.JobImageRefresh, LibraryID: library, Enabled: true, Timing: hourly}, cal); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous schedule", err)
	}

	// Outside its window a due occurrence queues nothing and waits for the
	// window's next opening.
	closed := domain.ScheduleWindow{Start: clockAfter(2 * time.Hour), End: clockAfter(3 * time.Hour)}
	images, err := f.s.PutJobSchedule(f.ctx, f.a, "", domain.JobScheduleInput{Kind: domain.JobImageRefresh, LibraryID: library, Enabled: true, Timing: hourly, Window: closed}, cal)
	if err != nil || images.Revision != 1 || images.NextDue == nil || images.NextDue.Before(time.Now().Add(time.Hour+50*time.Minute)) {
		t.Fatal("create inside a later window", images, err)
	}
	if _, err = f.s.PutJobSchedule(f.ctx, f.a, "", domain.JobScheduleInput{Kind: domain.JobImageRefresh, LibraryID: library, Enabled: true, Timing: hourly}, cal); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("second schedule of the same kind and library", err)
	}
	dueJobSchedule(t, f, images.ID)
	caps := app.ScheduleCapabilities{ImageRefresh: true}
	if worked, err := dispatchJobSchedules(f, caps); err != nil || !worked {
		t.Fatal("window dispatch", worked, err)
	}
	if scheduleCount(t, f, `SELECT count(*) FROM jobs`) != 0 {
		t.Fatal("job queued outside the window")
	}
	images, err = f.s.GetJobSchedule(f.ctx, f.a, images.ID)
	if err != nil || images.NextDue == nil || images.NextDue.Before(time.Now().Add(time.Hour+58*time.Minute)) || images.NextDue.After(time.Now().Add(2*time.Hour+time.Minute)) || images.LastJobID != "" {
		t.Fatal("occurrence not moved to the window opening", images, err)
	}
	if worked, err := dispatchJobSchedules(f, caps); err != nil || worked {
		t.Fatal("moved occurrence dispatched again", worked, err)
	}

	// Inside the window it queues the job as the system actor.
	open := domain.ScheduleWindow{Start: clockAfter(-time.Hour), End: clockAfter(time.Hour)}
	if _, err = f.s.PutJobSchedule(f.ctx, f.a, images.ID, domain.JobScheduleInput{Kind: domain.JobImageRefresh, LibraryID: library, Enabled: true, Timing: hourly, Window: closed}, cal); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	images, err = f.s.PutJobSchedule(f.ctx, f.a, images.ID, domain.JobScheduleInput{Kind: domain.JobImageRefresh, LibraryID: library, ExpectedRevision: images.Revision, Enabled: true, Timing: hourly, Window: open}, cal)
	if err != nil || images.Revision != 2 {
		t.Fatal("replace", images, err)
	}
	dueJobSchedule(t, f, images.ID)
	if worked, err := dispatchJobSchedules(f, caps); err != nil || !worked {
		t.Fatal("dispatch inside the window", worked, err)
	}
	images, err = f.s.GetJobSchedule(f.ctx, f.a, images.ID)
	if err != nil || images.LastJobID == "" || images.LastRunAt == nil || images.NextDue == nil || !images.NextDue.After(time.Now().Add(50*time.Minute)) {
		t.Fatal("dispatched schedule", images, err)
	}
	if j := f.get(t, images.LastJobID); j.Kind != domain.JobImageRefresh || j.Priority != domain.JobPriorityBackground {
		t.Fatal("scheduled job", j)
	}
	if scheduleCount(t, f, `SELECT count(*) FROM jobs WHERE id=$1::uuid AND actor_id IS NULL AND system_source=$2`, images.LastJobID, "system:schedule:"+images.ID) != 1 ||
		scheduleCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='image.refresh_submitted' AND target_id=$1::uuid AND actor_id IS NULL AND request_id=$2 AND after_state->>'scheduleId'=$3`, images.LastJobID, "system:schedule:"+images.ID, images.ID) != 1 {
		t.Fatal("scheduled job not attributed to the system")
	}

	// A busy library keeps the occurrence for a retry.
	dueJobSchedule(t, f, images.ID)
	if worked, err := dispatchJobSchedules(f, caps); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if images, err = f.s.GetJobSchedule(f.ctx, f.a, images.ID); err != nil || images.LastError != domain.ScheduleErrorAdmission || images.RetryAfter == nil || !images.NextDue.Before(time.Now()) {
		t.Fatal("busy occurrence lost", images, err)
	}
	finishActiveJobs(t, f)

	// A kind this server cannot run skips the occurrence.
	cleanup, err := f.s.PutJobSchedule(f.ctx, f.a, "", domain.JobScheduleInput{Kind: domain.JobSubtitleCacheCleanup, Enabled: true, Timing: hourly, Params: domain.JobScheduleParams{MaxIdleDays: 9}}, cal)
	if err != nil || cleanup.LibraryID != "" || cleanup.Params.MaxIdleDays != 9 {
		t.Fatal("server schedule", cleanup, err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE job_schedules SET enabled=false,next_due=NULL WHERE id=$1::uuid`, images.ID); err != nil {
		t.Fatal(err)
	}
	dueJobSchedule(t, f, cleanup.ID)
	if worked, err := dispatchJobSchedules(f, app.ScheduleCapabilities{}); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if cleanup, err = f.s.GetJobSchedule(f.ctx, f.a, cleanup.ID); err != nil || cleanup.LastError != domain.ScheduleErrorKindUnavailable || cleanup.NextDue == nil || !cleanup.NextDue.After(time.Now()) || scheduleCount(t, f, `SELECT count(*) FROM jobs WHERE kind='subtitle_cache_cleanup'`) != 0 {
		t.Fatal("unavailable kind", cleanup, err)
	}

	// A once schedule runs once and turns itself off.
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	once := domain.ScheduleTiming{Mode: domain.ScheduleModeOnce, Timezone: "Asia/Taipei", RunAt: &at}
	reminder, err := f.s.PutJobSchedule(f.ctx, f.a, "", domain.JobScheduleInput{Kind: domain.JobBackupReminder, Enabled: true, Timing: once, Params: domain.JobScheduleParams{ThresholdDays: 3}}, cal)
	if err != nil || reminder.NextDue == nil || !reminder.NextDue.Equal(at) {
		t.Fatal("once schedule", reminder, err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE job_schedules SET run_at=clock_timestamp()-interval '1 second',next_due=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, reminder.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := dispatchJobSchedules(f, app.ScheduleCapabilities{BackupReminderDays: 7}); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if reminder, err = f.s.GetJobSchedule(f.ctx, f.a, reminder.ID); err != nil || reminder.Enabled || reminder.NextDue != nil || reminder.LastJobID == "" {
		t.Fatal("once schedule stayed enabled", reminder, err)
	}
	if scheduleCount(t, f, `SELECT count(*) FROM backup_reminder_job_state WHERE job_id=$1::uuid AND trigger='schedule' AND threshold_days=3`, reminder.LastJobID) != 1 {
		t.Fatal("scheduled reminder state")
	}
	list, err := f.s.ListJobSchedules(f.ctx, f.a, "")
	if err != nil || len(list) != 3 || list[0].LibraryID != "" {
		t.Fatal("list", len(list), err)
	}
	if list, err = f.s.ListJobSchedules(f.ctx, f.a, library); err != nil || len(list) != 1 || list[0].ID != images.ID {
		t.Fatal("library list", list, err)
	}
	if err = f.s.DeleteJobSchedule(f.ctx, f.a, cleanup.ID, cleanup.Revision+1); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale delete", err)
	}
	if err = f.s.DeleteJobSchedule(f.ctx, f.a, cleanup.ID, cleanup.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetJobSchedule(f.ctx, f.a, cleanup.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("deleted schedule", err)
	}
	for _, event := range []string{"schedule.created", "schedule.updated", "schedule.deleted"} {
		if scheduleCount(t, f, `SELECT count(*) FROM audit_logs WHERE event=$1 AND actor_id=$2::uuid`, event, f.a.UserID) == 0 {
			t.Fatal("schedule change not audited", event)
		}
	}
	// A schedule schema 105 cannot express refuses the downgrade.
	refuseRetainedDowngrade(t, f, "job_schedules", "generic schedule silently downgraded")
}

// A scheduled metadata refresh runs as the system actor: it succeeds while
// no administrator has any session, where a manual job of the same kind
// fails because it runs as its submitter.
func TestJobScheduleMetadataRefreshWithoutSessions(t *testing.T) {
	f := newJobFixtureWithin(t, 3*time.Minute)
	provider := newRefreshTMDB()
	provider.add(949, "Heat", "1995-12-15")
	metadata := refreshTestMetadata(t, f, provider)
	heat := refreshTestItem(t, f, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	refresh, err := f.s.PutJobSchedule(f.ctx, f.a, "", domain.JobScheduleInput{Kind: domain.JobMetadataRefresh, LibraryID: f.registration.Library.ID, Enabled: true,
		Timing: domain.ScheduleTiming{Mode: domain.ScheduleModeCron, Cron: "0 4 * * *", Timezone: "America/New_York"}}, calendar.Calendar{})
	if err != nil {
		t.Fatal(err)
	}
	manual := submitMetadataRefresh(t, f, "manual-before-logout")
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	stop := startMetadataRefreshRunner(t, f, metadata, f.s, false)
	defer stop()
	if state, code := waitTerminalJob(t, f, manual.ID); state != domain.JobFailed || code != "metadata_refresh_failed" {
		t.Fatal("manual refresh ran without a session", state, code)
	}
	// Without a provider the occurrence is skipped.
	dueJobSchedule(t, f, refresh.ID)
	if worked, err := dispatchJobSchedules(f, app.ScheduleCapabilities{}); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if scheduleCount(t, f, `SELECT count(*) FROM jobs WHERE kind='metadata_refresh'`) != 1 {
		t.Fatal("refresh queued without a provider")
	}
	dueJobSchedule(t, f, refresh.ID)
	if worked, err := dispatchJobSchedules(f, app.ScheduleCapabilities{MetadataRefresh: true}); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var job string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT last_job_id::text FROM job_schedules WHERE id=$1::uuid`, refresh.ID).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if state, code := waitTerminalJob(t, f, job); state != domain.JobSucceeded {
		t.Fatal("scheduled refresh without sessions", state, code)
	}
	source := "system:schedule:" + refresh.ID
	if scheduleCount(t, f, `SELECT count(*) FROM metadata_refresh_job_state WHERE job_id=$1::uuid AND trigger='schedule' AND applied=1`, job) != 1 {
		t.Fatal("scheduled refresh did not apply")
	}
	// The administrator has no session left; read as the schedule's actor.
	md, err := f.s.ItemMetadata(f.ctx, domain.SystemActor(domain.SystemTriggerSchedule, refresh.ID), heat)
	if err != nil {
		t.Fatal(err)
	}
	refreshed := false
	for _, field := range md.Fields {
		refreshed = refreshed || field.Field == "overview" && field.ProviderOrigin != nil && field.ProviderOrigin.ProviderID == 949
	}
	if !refreshed {
		t.Fatal("item not refreshed", md.Fields)
	}
	// Every change it made is audited to the system with the schedule.
	if scheduleCount(t, f, `SELECT count(*) FROM audit_logs WHERE target_id=$1::uuid AND actor_id IS NULL AND request_id=$2`, heat, source) == 0 ||
		scheduleCount(t, f, `SELECT count(*) FROM audit_logs WHERE target_id=$1::uuid AND actor_id IS NOT NULL AND occurred_at>(SELECT created_at FROM jobs WHERE id=$2::uuid)`, heat, job) != 0 {
		t.Fatal("refresh changes not audited to the system")
	}
}
