package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestJobsStatusReadDoesNotWaitForWorkerLock(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "status-reader")
	tx, err := f.s.jobTransaction(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	ctx, cancel := context.WithTimeout(f.ctx, 250*time.Millisecond)
	defer cancel()
	read, err := f.s.GetJob(ctx, f.a, j.ID)
	if err != nil || read.ID != j.ID {
		t.Fatal("status read waited for the worker write lock", err)
	}
	invalid := f.a
	invalid.SessionID = "00000000-0000-4000-8000-000000000001"
	if _, err = f.s.GetJob(f.ctx, invalid, j.ID); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("status read lost live session validation", err)
	}
}

func TestInventorySnapshotGarbageCollectionIsBounded(t *testing.T) {
	f, l := snapshotFixture(t, 1)
	var stale int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT nextval('inventory_snapshot_sequence')`).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO library_inventory_baseline_data(library_id,root_id,snapshot_id,path) SELECT $1::uuid,$2::uuid,$3,'stale-'||n FROM generate_series(1,1000)n`, f.registration.Library.ID, f.registration.RootID, stale); err != nil {
		t.Fatal(err)
	}
	if ready, err := f.s.PrepareInventoryPublication(f.ctx, l); err != nil || ready {
		t.Fatal("cleanup finished early", ready, err)
	}
	var left int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline_data WHERE snapshot_id=$1`, stale).Scan(&left); err != nil || left != 488 {
		t.Fatal("cleanup batch bound", left, err)
	}
	if snapshotCount(t, f) != 1 {
		t.Fatal("cleanup removed visible baseline")
	}
	prepareSnapshot(t, f, l)
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline_data WHERE snapshot_id=$1`, stale).Scan(&left); err != nil || left != 0 {
		t.Fatal("stale data retained", left, err)
	}
	if snapshotCount(t, f) != 1 {
		t.Fatal("pending snapshot became visible during cleanup")
	}
}

func snapshotFixture(t *testing.T, count int, setup ...func(*testing.T, jobFixture)) (jobFixture, domain.JobLease) {
	t.Helper()
	f := newJobFixture(t, setup...)
	f.complete(t, "snapshot-old", []string{"keep.mkv"}, 0)
	f.submit(t, "snapshot-new")
	l := f.claim(t, "snapshot-owner")
	d := f.directory(t, l)
	for offset := 0; offset < count; {
		var entries []domain.InventoryEntry
		for len(entries) < domain.ScanBatchMaxEntries && offset < count {
			name := fmt.Sprintf("new-%04d.mkv", offset)
			if offset == 0 {
				name = "keep.mkv"
			}
			entries = append(entries, scanEntry(d, name, 2))
			offset++
		}
		if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: entries, Done: offset == count}); err != nil {
			t.Fatal(err)
		}
	}
	return f, l
}

func snapshotCount(t *testing.T, f jobFixture) int {
	t.Helper()
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func prepareSnapshot(t *testing.T, f jobFixture, l domain.JobLease) {
	t.Helper()
	for i := 0; i < 100; i++ {
		ready, err := f.s.PrepareInventoryPublication(f.ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			return
		}
	}
	t.Fatal("snapshot preparation did not finish")
}

func TestInventorySnapshotInvisibleUntilPublishAndResume(t *testing.T) {
	f, l := snapshotFixture(t, 300)
	ready, err := f.s.PrepareInventoryPublication(f.ctx, l)
	if err != nil || ready {
		t.Fatal("first batch", ready, err)
	}
	var copied int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT copied FROM inventory_snapshot_preparations WHERE job_id=$1::uuid`, l.Job.ID).Scan(&copied); err != nil || copied != 128 {
		t.Fatal("unbounded stage", copied, err)
	}
	if snapshotCount(t, f) != 1 {
		t.Fatal("partial snapshot visible")
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unready snapshot published", err)
	}
	if err = f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	current := f.claim(t, "snapshot-replacement")
	if _, err = f.s.PrepareInventoryPublication(f.ctx, l); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("old owner staged", err)
	}
	prepareSnapshot(t, f, current)
	if snapshotCount(t, f) != 1 {
		t.Fatal("ready snapshot visible before commit")
	}
	if err = f.s.FinishJob(f.ctx, current, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if snapshotCount(t, f) != 300 {
		t.Fatal("published count")
	}
	var snapshots int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(DISTINCT snapshot_id) FROM library_inventory_baseline_data WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&snapshots); err != nil || snapshots != 2 {
		t.Fatal("old snapshot was removed in publishing transaction", snapshots, err)
	}
}

func TestInventorySnapshotInvalidationAndCancellation(t *testing.T) {
	for _, kind := range []string{"roots", "baseline", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			f, l := snapshotFixture(t, 300)
			if _, err := f.s.PrepareInventoryPublication(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			want := domain.ErrInventoryInvalidated
			switch kind {
			case "roots":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET inventory_generation=inventory_generation+1 WHERE id=$1::uuid`, f.registration.Library.ID); err != nil {
					t.Fatal(err)
				}
			case "baseline":
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid`, f.registration.Library.ID); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				want = context.Canceled
				if _, err := f.s.CancelJob(f.ctx, f.a, l.Job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.PrepareInventoryPublication(f.ctx, l); !errors.Is(err, want) {
				t.Fatal("invalid preparation accepted", err)
			}
			if snapshotCount(t, f) != 1 {
				t.Fatal("invalid preparation changed visible baseline")
			}
		})
	}
}

func TestInventorySnapshotLeaseExpiresDuringStageAndPublish(t *testing.T) {
	t.Run("stage", func(t *testing.T) {
		f, l := snapshotFixture(t, 1)
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION slow_snapshot_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END $$; CREATE TRIGGER slow_snapshot BEFORE INSERT ON library_inventory_baseline_data FOR EACH ROW EXECUTE FUNCTION slow_snapshot_insert()`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.PrepareInventoryPublication(f.ctx, l); !errors.Is(err, domain.ErrJobLeaseLost) {
			t.Fatal("expired stage committed", err)
		}
		var staged int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM inventory_snapshot_preparations`).Scan(&staged); err != nil || staged != 0 || snapshotCount(t, f) != 1 {
			t.Fatal("expired stage retained state", staged, err)
		}
	})
	t.Run("publish", func(t *testing.T) {
		f, l := snapshotFixture(t, 1)
		prepareSnapshot(t, f, l)
		if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION slow_snapshot_publish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END $$; CREATE TRIGGER slow_snapshot BEFORE UPDATE ON libraries FOR EACH ROW WHEN(OLD.active_inventory_snapshot<>NEW.active_inventory_snapshot) EXECUTE FUNCTION slow_snapshot_publish()`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrJobLeaseLost) {
			t.Fatal("expired snapshot published", err)
		}
		var active int64
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT active_inventory_snapshot FROM libraries WHERE id=$1::uuid`, f.registration.Library.ID).Scan(&active); err != nil || active != 0 {
			t.Fatal("expired publication changed pointer", active, err)
		}
		if f.get(t, l.Job.ID).State != domain.JobRunning {
			t.Fatal("expired publication changed job state")
		}
	})
}

func TestInventorySnapshotMigrationAndVisibleDML(t *testing.T) {
	ctx, s, dsn := accountTestStore(t)
	legacyMigrationStoreAt44(t, ctx, s)
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 43 {
		t.Fatal("empty ignored snapshot down", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 42 {
		t.Fatal("empty snapshot down", v, dirty, err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "up"); err != nil || dirty || v != SchemaVersion {
		t.Fatal("snapshot up", v, dirty, err)
	}
	// Account code reads columns newer than schema 44, so the actor is
	// created on the latest schema before the legacy downgrade.
	if _, err := s.BootstrapAdmin(ctx, accountInput("snapshot-migration")); err != nil {
		t.Fatal(err)
	}
	a := accountActor(accountLogin(t, ctx, s, "snapshot-migration"))
	legacyMigrationStoreAt44(t, ctx, s)
	r, err := s.RegisterLibrary(ctx, "snapshots", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := jobFixture{ctx: ctx, s: s, a: a, registration: r, policy: jobTestPolicy()}
	f.submit(t, "empty-snapshot")
	l := f.claim(t, "empty-owner")
	d := f.directory(t, l)
	if err = s.SaveScanBatch(ctx, l, d, domain.ScanBatch{Done: true}); err != nil {
		t.Fatal(err)
	}
	prepareSnapshot(t, f, l)
	if err = s.FinishJob(ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO library_inventory_baseline(library_id,root_id,path) VALUES($1::uuid,$2::uuid,'legacy-row')`, r.Library.ID, r.RootID); err != nil {
		t.Fatal("view insert", err)
	}
	if snapshotCount(t, f) != 1 {
		t.Fatal("view insert invisible")
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE library_inventory_baseline SET path='updated' WHERE library_id=$1::uuid`, r.Library.ID); err != nil {
		t.Fatal("view update", err)
	}
	if v, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || v != 43 {
		t.Fatal("plain preparation down", v, dirty, err)
	}
	if _, _, err = Migrate(ctx, dsn, "down"); err == nil {
		t.Fatal("retained snapshot downgraded")
	}
}
