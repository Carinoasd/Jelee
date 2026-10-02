package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/calendar"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	worker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

const watchOwnerA = "11111111-1111-4111-8111-111111111111"
const watchOwnerB = "22222222-2222-4222-8222-222222222222"

func watchInput() domain.ScanScheduleInput {
	v := scheduleInput()
	v.Enabled = false
	v.Watch = true
	return v
}
func watchFixture(t *testing.T) jobFixture {
	t.Helper()
	f := newJobFixture(t)
	if _, err := f.s.PutScanSchedule(f.ctx, f.a, f.registration.Library.ID, watchInput(), calendar.Calendar{}); err != nil {
		t.Fatal(err)
	}
	return f
}
func claimWatch(t *testing.T, f jobFixture, owner string) domain.WatchLease {
	t.Helper()
	v, err := f.s.ClaimWatch(f.ctx, owner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func dispatchWatch(f jobFixture, v domain.WatchLease) (bool, error) {
	return f.s.DispatchWatch(f.ctx, v, f.policy, nil, nil, app.IgnoreAdmissionCapabilities{})
}

func TestWatchDurableDirtyAndBusyDispatch(t *testing.T) {
	f := watchFixture(t)
	lease := claimWatch(t, f, watchOwnerA)
	status, err := f.s.GetWatchStatus(f.ctx, f.a, lease.LibraryID)
	if err != nil || status.Observing || status.Pending {
		t.Fatal("unarmed observer reported ready", status, err)
	}
	for range 2 {
		if err = f.s.MarkWatchDirty(f.ctx, lease); err != nil {
			t.Fatal(err)
		}
	}
	if worked, err := dispatchWatch(f, lease); err != nil || !worked {
		t.Fatal(worked, err)
	}
	status, err = f.s.GetWatchStatus(f.ctx, f.a, lease.LibraryID)
	if err != nil || !status.Observing || status.Pending || status.LastJobID == "" {
		t.Fatal("dirty state not consumed", status, err)
	}
	first := status.LastJobID
	if err = f.s.MarkWatchDirty(f.ctx, lease); err != nil {
		t.Fatal(err)
	}
	if worked, err := dispatchWatch(f, lease); err != nil || !worked {
		t.Fatal(worked, err)
	}
	status, err = f.s.GetWatchStatus(f.ctx, f.a, lease.LibraryID)
	if err != nil || !status.Pending || status.LastJobID != first || status.LastError != "admission_unavailable" {
		t.Fatal("busy scan lost later event", status, err)
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, first); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE scan_watch_state SET retry_after=NULL`); err != nil {
		t.Fatal(err)
	}
	if worked, err := dispatchWatch(f, lease); err != nil || !worked {
		t.Fatal(worked, err)
	}
	status, err = f.s.GetWatchStatus(f.ctx, f.a, lease.LibraryID)
	if err != nil || status.Pending || status.LastJobID == first {
		t.Fatal("pending event did not resume", status, err)
	}
	input := watchInput()
	input.ExpectedRevision = 1
	input.Watch = false
	if _, err = f.s.PutScanSchedule(f.ctx, f.a, lease.LibraryID, input, calendar.Calendar{}); err != nil {
		t.Fatal(err)
	}
	if err = f.s.MarkWatchDirty(f.ctx, lease); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("disabled watch wrote", err)
	}
	if live, err := f.s.RenewWatch(f.ctx, lease, time.Minute); err != nil || live {
		t.Fatal("disabled watch renewed", live, err)
	}
}

func TestWatchCompetingClaimsAndStaleFences(t *testing.T) {
	f := watchFixture(t)
	var wg sync.WaitGroup
	leases := make(chan domain.WatchLease, 2)
	failures := make(chan error, 2)
	for _, owner := range []string{watchOwnerA, watchOwnerB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := f.s.ClaimWatch(f.ctx, owner, time.Minute)
			if err == nil {
				leases <- v
			} else {
				failures <- err
			}
		}()
	}
	wg.Wait()
	close(leases)
	close(failures)
	if len(leases) != 1 || len(failures) != 1 {
		t.Fatal("multiple observation owners", len(leases), len(failures))
	}
	first := <-leases
	if err := <-failures; !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE scan_watch_state SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	second := claimWatch(t, f, watchOwnerB)
	if second.Generation <= first.Generation {
		t.Fatal("owner generation not advanced")
	}
	if err := f.s.MarkWatchDirty(f.ctx, first); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("stale mark", err)
	}
	if _, err := dispatchWatch(f, first); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("stale dispatch", err)
	}
	if err := f.s.ReleaseWatch(f.ctx, first, "observer_unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.MarkWatchDirty(f.ctx, second); err != nil {
		t.Fatal("stale release cleared new owner", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET inventory_generation=inventory_generation+1 WHERE id=$1::uuid`, second.LibraryID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.MarkWatchDirty(f.ctx, second); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("changed roots accepted old observer", err)
	}
	third := claimWatch(t, f, watchOwnerA)
	input := watchInput()
	input.ExpectedRevision = 1
	if _, err := f.s.PutScanSchedule(f.ctx, f.a, third.LibraryID, input, calendar.Calendar{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchWatch(f, third); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("changed definition accepted old observer", err)
	}
}

func TestWatchLeaseExpiryRollsBackMarkAndJob(t *testing.T) {
	f := watchFixture(t)
	lease, err := f.s.ClaimWatch(f.ctx, watchOwnerA, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `CREATE FUNCTION delay_watch_dirty() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.dirty_generation>OLD.dirty_generation THEN PERFORM pg_sleep(1.1); END IF; RETURN NEW; END $$; CREATE TRIGGER delay_watch_dirty BEFORE UPDATE ON scan_watch_state FOR EACH ROW EXECUTE FUNCTION delay_watch_dirty()`)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.MarkWatchDirty(f.ctx, lease); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("expired dirty write", err)
	}
	var dirty int64
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT dirty_generation FROM scan_watch_state`).Scan(&dirty); err != nil || dirty != 0 {
		t.Fatal("dirty state survived rollback", dirty, err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER delay_watch_dirty ON scan_watch_state`); err != nil {
		t.Fatal(err)
	}
	lease, err = f.s.ClaimWatch(f.ctx, watchOwnerB, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.MarkWatchDirty(f.ctx, lease); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `CREATE FUNCTION delay_watch_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END $$; CREATE TRIGGER delay_watch_job BEFORE INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION delay_watch_job()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dispatchWatch(f, lease); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("expired job publication", err)
	}
	var count int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("job survived expired lease", count, err)
	}
}

func TestWatchCapacityAndOwnerLoss(t *testing.T) {
	f := watchFixture(t)
	for i := 1; i <= domain.MaxWatchLibraries; i++ {
		library, err := f.s.RegisterLibrary(f.ctx, fmt.Sprintf("watch-%d", i), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.s.PutScanSchedule(f.ctx, f.a, library.Library.ID, watchInput(), calendar.Calendar{})
		if i == domain.MaxWatchLibraries {
			if !errors.Is(err, domain.ErrScanLimit) {
				t.Fatal("watch capacity not bounded", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	reader := accountInput("watch-reader")
	reader.Admin = true
	if _, _, err := f.s.CreateUser(f.ctx, f.a, reader, "watch-reader"); err != nil {
		t.Fatal(err)
	}
	actor := accountActor(accountLogin(t, f.ctx, f.s, "watch-reader"))
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ClaimWatch(f.ctx, watchOwnerA, time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("disabled owner claimed", err)
	}
	status, err := f.s.GetWatchStatus(f.ctx, actor, f.registration.Library.ID)
	if err != nil || status.Observing || status.LastError != "owner_unavailable" {
		t.Fatal("lost owner status", status, err)
	}
}

func TestWatchActualObserverWorkersAndRestart(t *testing.T) {
	f := watchFixture(t)
	service, err := app.NewJobs(f.s, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	service, err = app.NewJobsWithSchedules(service, f.s, calendar.Calendar{})
	if err != nil {
		t.Fatal(err)
	}
	service, err = app.NewJobsWithWatch(service, f.s)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := scan.NewDirectoryWatcher(scan.WatchOptions{MaxDirectories: 16, QuietPeriod: 100 * time.Millisecond, MaxDelay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	options := worker.DefaultOptions()
	options.Workers = 1
	options.PollInterval = 100 * time.Millisecond
	scanner, err := worker.New(f.s, scan.New(), options, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err = scanner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	stop := func(value interface{ Stop(context.Context) error }) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := value.Stop(ctx); err != nil {
			t.Error(err)
		}
	}
	defer stop(scanner)
	start := func() *worker.WatchRunner {
		t.Helper()
		runner, err := worker.NewWatchRunner(f.s, observer, service, logger)
		if err != nil {
			t.Fatal(err)
		}
		if err = runner.Start(f.ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stop(runner) })
		return runner
	}
	first, standby := start(), start()
	waitJob := func(previous string, files int64) string {
		t.Helper()
		deadline := time.NewTimer(25 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			status, err := service.WatchStatus(f.ctx, f.a, f.registration.Library.ID)
			if err != nil {
				t.Fatal(err)
			}
			if status.LastJobID != "" && status.LastJobID != previous {
				job := f.get(t, status.LastJobID)
				if job.State == domain.JobSucceeded && job.Files == files {
					return job.ID
				}
				if job.State == domain.JobFailed {
					t.Fatal("watch scan failed", job.ErrorCode)
				}
			}
			select {
			case <-deadline.C:
				t.Fatal("watch did not produce completed scan")
			case <-tick.C:
			}
		}
	}
	initial := waitJob("", 0)
	var root string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "watched.mkv")
	original := []byte("unchanged watched video")
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		if err = os.WriteFile(filepath.Join(root, fmt.Sprintf("burst-%03d.mkv", i)), []byte("burst"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	changed := waitJob(initial, 201)
	stop(first)
	stop(standby)
	start()
	waitJob(changed, 201)
	if value, err := os.ReadFile(path); err != nil || string(value) != string(original) {
		t.Fatal("watch changed media", err)
	}
}

func TestWatchMigration(t *testing.T) {
	ctx, s, dsn := accountTestStore(t)
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 41 {
		t.Fatal("empty watch down", version, dirty, err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("watch up", version, dirty, err)
	}
	if _, err := s.BootstrapAdmin(ctx, accountInput("watch-migration")); err != nil {
		t.Fatal(err)
	}
	a := accountActor(accountLogin(t, ctx, s, "watch-migration"))
	library, err := s.RegisterLibrary(ctx, "watch", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutScanSchedule(ctx, a, library.Library.ID, watchInput(), calendar.Calendar{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = Migrate(ctx, dsn, "down"); err == nil {
		t.Fatal("retained watch silently downgraded")
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM scan_watch_state`).Scan(&count); err != nil || count != 1 {
		t.Fatal("watch state lost", count, err)
	}
}
