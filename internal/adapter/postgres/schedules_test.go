package postgres

import (
	"errors"
	"github.com/MoYuanCN/Jelee/internal/adapter/calendar"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"sync"
	"testing"
	"time"
)

func scheduleInput() domain.ScanScheduleInput {
	return domain.ScanScheduleInput{Enabled: true, Timing: domain.ScheduleTiming{Mode: "interval", IntervalSeconds: 3600, Timezone: "Asia/Taipei"}}
}
func dueSchedule(t *testing.T, f jobFixture) {
	t.Helper()
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE scan_schedules SET next_due=clock_timestamp()-interval '7 days',retry_after=NULL`); err != nil {
		t.Fatal(err)
	}
}
func dispatchSchedule(f jobFixture) (bool, error) {
	return f.s.DispatchScanSchedule(f.ctx, f.policy, calendar.Calendar{}, nil, nil, app.IgnoreAdmissionCapabilities{})
}

func TestScheduleCompetingDispatchersAndRestart(t *testing.T) {
	f := newJobFixture(t)
	v, err := f.s.PutScanSchedule(f.ctx, f.a, f.registration.Library.ID, scheduleInput(), calendar.Calendar{})
	if err != nil || v.Revision != 1 || v.NextDue == nil {
		t.Fatal(v, err)
	}
	dueSchedule(t, f)
	// Each invocation opens its own transaction, just as separate instances do.
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := dispatchSchedule(f); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate/missing scheduled jobs", count, err)
	}
	v, err = f.s.GetScanSchedule(f.ctx, f.a, v.LibraryID)
	if err != nil || v.LastJobID == "" || v.NextDue == nil || !v.NextDue.After(time.Now()) || v.RetryAfter != nil {
		t.Fatal("missed runs not coalesced", v, err)
	}
	j := f.get(t, v.LastJobID)
	if j.Priority != domain.JobPriorityBackground {
		t.Fatal("wrong priority")
	}
	if worked, err := dispatchSchedule(f); err != nil || worked {
		t.Fatal("future occurrence replayed", worked, err)
	}
	// A later occurrence encounters the active job and retains its due time.
	dueSchedule(t, f)
	if worked, err := dispatchSchedule(f); err != nil || !worked {
		t.Fatal(worked, err)
	}
	v, err = f.s.GetScanSchedule(f.ctx, f.a, v.LibraryID)
	if err != nil || v.LastError != "admission_unavailable" || v.RetryAfter == nil || !v.NextDue.Before(time.Now()) {
		t.Fatal("busy occurrence lost", v, err)
	}
	if worked, err := dispatchSchedule(f); err != nil || worked {
		t.Fatal("retry delay ignored", worked, err)
	}
}

func TestScheduleAuthorityRevisionAndDisable(t *testing.T) {
	f := newJobFixture(t)
	input := scheduleInput()
	cal := calendar.Calendar{}
	if _, err := f.s.PutScanSchedule(f.ctx, domain.Actor{}, f.registration.Library.ID, input, cal); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous schedule", err)
	}
	v, err := f.s.PutScanSchedule(f.ctx, f.a, f.registration.Library.ID, input, cal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.PutScanSchedule(f.ctx, f.a, v.LibraryID, input, cal); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale overwrite", err)
	}
	input.ExpectedRevision = v.Revision
	input.Enabled = false
	v, err = f.s.PutScanSchedule(f.ctx, f.a, v.LibraryID, input, cal)
	if err != nil || v.Enabled || v.NextDue != nil {
		t.Fatal("disable", v, err)
	}
	if worked, err := dispatchSchedule(f); err != nil || worked {
		t.Fatal("disabled schedule dispatched", worked, err)
	}
	input.ExpectedRevision = v.Revision
	input.Enabled = true
	if _, err = f.s.PutScanSchedule(f.ctx, f.a, v.LibraryID, input, cal); err != nil {
		t.Fatal(err)
	}
	dueSchedule(t, f)
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	if worked, err := dispatchSchedule(f); err != nil || !worked {
		t.Fatal("logout cancelled durable intent", worked, err)
	}
	if _, err = f.s.GetScanSchedule(f.ctx, f.a, v.LibraryID); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("revoked session read", err)
	}
	dueSchedule(t, f)
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID); err != nil {
		t.Fatal(err)
	}
	if worked, err := dispatchSchedule(f); err != nil || !worked {
		t.Fatal("disabled owner dispatch", worked, err)
	}
	var enabled bool
	var code string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT enabled,last_error FROM scan_schedules`).Scan(&enabled, &code); err != nil || enabled || code != "owner_unavailable" {
		t.Fatal("owner remained enabled", enabled, code, err)
	}
}

func TestScheduleAdmissionFailureAndTransactionRollback(t *testing.T) {
	f := newJobFixture(t)
	input := scheduleInput()
	input.Probe = true
	v, err := f.s.PutScanSchedule(f.ctx, f.a, f.registration.Library.ID, input, calendar.Calendar{})
	if err != nil {
		t.Fatal(err)
	}
	dueSchedule(t, f)
	if worked, err := dispatchSchedule(f); err != nil || !worked {
		t.Fatal("unavailable capability", worked, err)
	}
	var count int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("disabled probe created job", count, err)
	}
	input.Probe = false
	input.ExpectedRevision = v.Revision
	if _, err = f.s.PutScanSchedule(f.ctx, f.a, v.LibraryID, input, calendar.Calendar{}); err != nil {
		t.Fatal(err)
	}
	dueSchedule(t, f)
	_, err = f.s.Pool.Exec(f.ctx, `CREATE FUNCTION fail_schedule_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.last_job_id IS NOT NULL THEN RAISE EXCEPTION 'fixture checkpoint failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_schedule_checkpoint BEFORE UPDATE ON scan_schedules FOR EACH ROW EXECUTE FUNCTION fail_schedule_checkpoint()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dispatchSchedule(f); err == nil {
		t.Fatal("checkpoint fault accepted")
	}
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("job survived failed due checkpoint", count, err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER fail_schedule_checkpoint ON scan_schedules`); err != nil {
		t.Fatal(err)
	}
	if worked, err := dispatchSchedule(f); err != nil || !worked {
		t.Fatal("retry after rollback", worked, err)
	}
}

func TestScheduleMigration(t *testing.T) {
	ctx, s, dsn := accountTestStore(t)
	legacyMigrationStoreAt44(t, ctx, s)
	var exists bool
	if err := s.Pool.QueryRow(ctx, `SELECT to_regclass('scan_schedules') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatal("schedule schema missing", err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 43 {
		t.Fatal("ignored snapshot migration down", version, dirty, err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 42 {
		t.Fatal("snapshot migration down", version, dirty, err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 41 {
		t.Fatal("empty schedule down", version, dirty, err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 40 {
		t.Fatal("empty schedule down", version, dirty, err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("schedule up", version, dirty, err)
	}

	if _, err := s.BootstrapAdmin(ctx, accountInput("schedule-migration")); err != nil {
		t.Fatal(err)
	}
	actor := accountActor(accountLogin(t, ctx, s, "schedule-migration"))
	library, err := s.RegisterLibrary(ctx, "schedule-migration", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutScanSchedule(ctx, actor, library.Library.ID, scheduleInput(), calendar.Calendar{}); err != nil {
		t.Fatal(err)
	}
	legacyMigrationStoreAt44(t, ctx, s)
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 43 {
		t.Fatal("ignored snapshot migration down", version, dirty, err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 42 {
		t.Fatal("snapshot migration down", version, dirty, err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 41 {
		t.Fatal("empty watch downgrade", version, dirty, err)
	}
	if _, _, err = Migrate(ctx, dsn, "down"); err == nil {
		t.Fatal("retained schedule silently downgraded")
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM scan_schedules`).Scan(&count); err != nil || count != 1 {
		t.Fatal("downgrade lost definition", count, err)
	}
}
