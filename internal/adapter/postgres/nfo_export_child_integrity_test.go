package postgres

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const insertExportChild = `INSERT INTO jobs(library_id,idempotency_key,kind,state,priority,export_job_id,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,finished_at) VALUES($1::uuid,$2,'nfo_write','cancelled','manual',$3::uuid,1,1,100,1,1,1,1,clock_timestamp())`

func requirePGCode(t *testing.T, what string, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("%s: want SQLSTATE %s, got %v", what, code, err)
	}
}

// attempt runs one statement in its own savepoint so a rejection leaves the
// surrounding transaction usable.
func attempt(t *testing.T, f jobFixture, tx pgx.Tx, query string, args ...any) error {
	t.Helper()
	if _, err := tx.Exec(f.ctx, `SAVEPOINT attempt`); err != nil {
		t.Fatal(err)
	}
	_, err := tx.Exec(f.ctx, query, args...)
	if err != nil {
		if _, e := tx.Exec(f.ctx, `ROLLBACK TO SAVEPOINT attempt`); e != nil {
			t.Fatal(e)
		}
		return err
	}
	if _, e := tx.Exec(f.ctx, `RELEASE SAVEPOINT attempt`); e != nil {
		t.Fatal(e)
	}
	return nil
}

func TestNFOExportChildMustNameAnExportJobOfItsLibrary(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A finished job of another kind in the same library.
	scan := f.submit(t, "scan")
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',finished_at=clock_timestamp() WHERE id=$1::uuid`, scan.ID); err != nil {
		t.Fatal(err)
	}
	nfoExportEnable(t, f, library)
	export := submitNFOExport(t, f, library, "parent")
	// The write-job trigger checks its request at commit; every statement here
	// is answered at once, and the transaction is rolled back.
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	const checkViolation, foreignKeyViolation = "23514", "23503"
	requirePGCode(t, "a job of another kind", attempt(t, f, tx, insertExportChild, library, "k1", scan.ID), checkViolation)
	requirePGCode(t, "an export job of another library", attempt(t, f, tx, insertExportChild, other.Library.ID, "k2", export.ID), checkViolation)
	requirePGCode(t, "a job that does not exist", attempt(t, f, tx, insertExportChild, library, "k3", "ffffffff-ffff-4fff-8fff-ffffffffffff"), foreignKeyViolation)
	if err = attempt(t, f, tx, insertExportChild, library, "ok", export.ID); err != nil {
		t.Fatal("the child of its export job was refused", err)
	}
	// Re-pointing or re-homing an existing child is checked the same way.
	requirePGCode(t, "re-pointing a child at another kind", attempt(t, f, tx, `UPDATE jobs SET export_job_id=$1::uuid WHERE idempotency_key='ok'`, scan.ID), checkViolation)
	requirePGCode(t, "moving a child to another library", attempt(t, f, tx, `UPDATE jobs SET library_id=$1::uuid WHERE idempotency_key='ok'`, other.Library.ID), checkViolation)
	// An export job with a child keeps its kind and library.
	requirePGCode(t, "changing the kind of a parent", attempt(t, f, tx, `UPDATE jobs SET kind='inventory_scan' WHERE id=$1::uuid`, export.ID), checkViolation)
	requirePGCode(t, "moving a parent", attempt(t, f, tx, `UPDATE jobs SET library_id=$1::uuid WHERE id=$2::uuid`, other.Library.ID, export.ID), checkViolation)
	// Clearing the pointer is always allowed.
	if err = attempt(t, f, tx, `UPDATE jobs SET export_job_id=NULL WHERE idempotency_key='ok'`); err != nil {
		t.Fatal("clearing the pointer was refused", err)
	}
}

// A real export, then history trim: the export job is older than the limit and
// is deleted, while the child write jobs keep their commit journal and cannot
// be deleted. The trim must go through and the children must lose only the
// pointer.
func TestNFOExportHistoryTrimKeepsJournaledChildren(t *testing.T) {
	f := newJobFixtureWithin(t, 5*time.Minute)
	metadata := nfoApplyTestMetadata(t, f)
	library := f.registration.Library.ID
	nfoExportLibrary(t, f, metadata, library, 24)
	job := submitNFOExport(t, f, library, "export-to-trim")
	stop := startNFOExportRunner(t, f, metadata, f.s)
	if done := waitNFOExportJob(t, f, job.ID); done.State != domain.JobSucceeded {
		t.Fatal("export did not succeed", done)
	}
	stop()
	children := nfoExportChildren(t, f, job.ID)
	if len(children) == 0 {
		t.Fatal("the export wrote nothing")
	}
	var journaled int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal j JOIN jobs c ON c.id=j.job_id WHERE c.export_job_id=$1::uuid`, job.ID).Scan(&journaled); err != nil || journaled == 0 {
		t.Fatal("the children have no commit journal to retain them", journaled, err)
	}
	f.policy.HistoryLimit = 1
	f.complete(t, "newer-1", []string{"a"}, 0)
	f.complete(t, "newer-2", []string{"a"}, 0)
	if _, err := f.s.GetJob(f.ctx, f.a, job.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("history trim kept the expired export job", err)
	}
	var state int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_export_job_state WHERE job_id=$1::uuid`, job.ID).Scan(&state); err != nil || state != 0 {
		t.Fatal("the export state outlived its job", state, err)
	}
	for _, child := range children {
		var pointer *string
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT export_job_id::text FROM jobs WHERE id=$1::uuid`, child.ID).Scan(&pointer); err != nil || pointer != nil {
			t.Fatal("a journaled child was deleted or kept its pointer", child.ID, pointer, err)
		}
	}
}

// An export job that is finished but still has an active child is not trimmed:
// deleting it would turn the child into a second active top-level job.
func TestNFOExportHistoryTrimKeepsAnExportWithAnActiveChild(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	nfoExportEnable(t, f, library)
	export := submitNFOExport(t, f, library, "parent")
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',finished_at=clock_timestamp() WHERE id=$1::uuid`, export.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs DISABLE TRIGGER verify_nfo_write_job`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO jobs(library_id,idempotency_key,kind,state,priority,export_job_id,owner,lease_until,started_at,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,'child','nfo_write','running','manual',$2::uuid,'w',clock_timestamp()+interval '1 hour',clock_timestamp(),1,1,100,1,1,1,1)`, library, export.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs ENABLE TRIGGER verify_nfo_write_job`); err != nil {
		t.Fatal(err)
	}
	// The library of the export has an active job (the child); the trim
	// is global, so finish jobs of another library.
	other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.policy.HistoryLimit = 1
	for i := 0; i < 2; i++ {
		job, _, err := f.s.SubmitJob(f.ctx, f.a, other.Library.ID, fmt.Sprintf("newer-%d", i), domain.JobPriorityManual, f.policy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',finished_at=clock_timestamp() WHERE id=$1::uuid`, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.GetJob(f.ctx, f.a, export.ID); err != nil {
		t.Fatal("the export job of an active child was trimmed", err)
	}
}

func TestNFOExportChildIntegrityMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	nfoExportEnable(t, f, library)
	export := submitNFOExport(t, f, library, "parent")
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',finished_at=clock_timestamp() WHERE id=$1::uuid`, export.ID); err != nil {
		t.Fatal(err)
	}
	insert := func(key string, parent string) {
		t.Helper()
		if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs DISABLE TRIGGER verify_nfo_write_job`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, insertExportChild, library, key, parent); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs ENABLE TRIGGER verify_nfo_write_job`); err != nil {
			t.Fatal(err)
		}
	}
	insert("good", export.ID)
	exists := `SELECT count(*) FROM pg_constraint WHERE conname='jobs_export_job_fkey'`
	triggers := `SELECT count(*) FROM pg_trigger WHERE tgname IN ('guard_job_export_parent','guard_job_export_children')`
	var fk, tg int
	if err := f.s.Pool.QueryRow(f.ctx, exists).Scan(&fk); err != nil || fk != 1 {
		t.Fatal("foreign key missing", err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, triggers).Scan(&tg); err != nil || tg != 2 {
		t.Fatal("guards missing", err)
	}
	jobMetricMigration(t, f, "down", uint(migrationVersion(t, "nfo_export_child_integrity")-1))
	if err := f.s.Pool.QueryRow(f.ctx, exists).Scan(&fk); err != nil || fk != 0 {
		t.Fatal("down left the foreign key", err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, triggers).Scan(&tg); err != nil || tg != 0 {
		t.Fatal("down left the guards", err)
	}
	// The older schema accepts a dangling pointer, as it did.
	insert("dangling", "ffffffff-ffff-4fff-8fff-ffffffffffff")
	// The fixture children have no write request; real ones do, and the
	// migration lets the write-job trigger verify the rows it updates.
	if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs DISABLE TRIGGER verify_nfo_write_job`); err != nil {
		t.Fatal(err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs ENABLE TRIGGER verify_nfo_write_job`); err != nil {
		t.Fatal(err)
	}
	// Upgrading keeps the child and clears only the pointer that could not hold.
	var good, dangling *string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT export_job_id::text FROM jobs WHERE idempotency_key='good'),(SELECT export_job_id::text FROM jobs WHERE idempotency_key='dangling')`).Scan(&good, &dangling); err != nil {
		t.Fatal(err)
	}
	if good == nil || *good != export.ID || dangling != nil {
		t.Fatal("pointers after upgrade", good, dangling)
	}
	if err := f.s.Pool.QueryRow(f.ctx, exists).Scan(&fk); err != nil || fk != 1 {
		t.Fatal("up did not restore the foreign key", err)
	}
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("round-trip schema is not ready", err)
	}
}
