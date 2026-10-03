package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestJobMetricsIntegrationFixedStorage(t *testing.T) {
	f := newJobFixture(t)
	var epoch, totals, buckets int
	err := f.s.Pool.QueryRow(f.ctx, `SELECT
	 (SELECT count(*) FROM job_metric_epoch),
	 (SELECT count(*) FROM job_metric_totals),
	 (SELECT count(*) FROM job_metric_buckets)`).Scan(&epoch, &totals, &buckets)
	if err != nil {
		t.Fatal("durable job metrics schema unavailable:", err)
	}
	if epoch != 1 || totals != 8 || buckets != 208 {
		t.Fatalf("fixed metric rows: epoch=%d totals=%d buckets=%d", epoch, totals, buckets)
	}
}

func jobMetricMigration(t *testing.T, f jobFixture, action string, want uint) {
	t.Helper()
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), action)
	for action == "down" && err == nil && !dirty && version > want {
		previous := version
		version, dirty, err = Migrate(f.ctx, f.s.Pool.Config().ConnString(), action)
		if err == nil && !dirty && version+1 != previous {
			t.Fatal("downgrade did not advance exactly one schema")
		}
	}
	if err != nil || dirty || version != want {
		t.Fatalf("metrics migration %s: version=%d dirty=%t error=%v", action, version, dirty, err)
	}
}

// Compare the durable rows themselves, including the epoch and exact numeric
// sums. Scrape timestamps and queue ages deliberately do not enter this check.
// The catalog_sync dimension exists only from schema58; its zero rows come and
// go with that migration and are excluded so older-schema comparisons hold.
func jobMetricMigrationStorage(t *testing.T, f jobFixture) string {
	t.Helper()
	var result string
	err := f.s.Pool.QueryRow(f.ctx, `SELECT jsonb_build_object(
	 'epoch',(SELECT jsonb_agg(to_jsonb(m) ORDER BY singleton) FROM job_metric_epoch m),
	 'totals',(SELECT jsonb_agg(to_jsonb(m) ORDER BY kind,priority) FROM job_metric_totals m WHERE kind<>'catalog_sync'),
	 'buckets',(SELECT jsonb_agg(to_jsonb(m) ORDER BY kind,priority,measure,bucket_index) FROM job_metric_buckets m WHERE kind<>'catalog_sync')
	)::text`).Scan(&result)
	if err != nil {
		t.Fatal("read durable metric rows:", err)
	}
	return result
}

func jobMetricMigrationJob(t *testing.T, f jobFixture, id string) string {
	t.Helper()
	var result string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, id).Scan(&result); err != nil {
		t.Fatal("read job before/after rejected transition:", err)
	}
	return result
}

func jobMetricMigrationZero(t *testing.T, f jobFixture) {
	t.Helper()
	var valid bool
	err := f.s.Pool.QueryRow(f.ctx, `SELECT
	 (SELECT count(*)=1 AND bool_and(singleton AND isfinite(started_at)) FROM job_metric_epoch)
	 AND (SELECT count(*)=(SELECT CASE WHEN version>=58 THEN 8 WHEN version>=47 THEN 6 ELSE 4 END FROM schema_migrations) AND bool_and(succeeded_total=0 AND failed_total=0 AND cancelled_total=0
	  AND wait_count=0 AND wait_sum_microseconds=0 AND duration_count=0 AND duration_sum_microseconds=0)
	  FROM job_metric_totals)
	 AND (SELECT count(*)=(SELECT CASE WHEN version>=58 THEN 208 WHEN version>=47 THEN 156 ELSE 104 END FROM schema_migrations) AND bool_and(bucket_count=0) FROM job_metric_buckets)`).Scan(&valid)
	if err != nil || !valid {
		t.Fatalf("migration did not establish an empty finite epoch: valid=%t error=%v", valid, err)
	}
}

func jobMetricMigrationRejected(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || len(pgErr.Code) != 5 || (pgErr.Code[:2] != "22" && pgErr.Code[:2] != "23") {
		t.Fatalf("expected data/constraint rejection, got %v", err)
	}
}

func TestJobMetricsMigrationEmptyRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	jobMetricMigration(t, f, "down", 46)
	jobMetricMigration(t, f, "down", 45)
	jobMetricMigrationZero(t, f)
	jobMetricMigration(t, f, "down", 44)
	var removed bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('job_metric_epoch') IS NULL
	 AND to_regclass('job_metric_totals') IS NULL AND to_regclass('job_metric_buckets') IS NULL
	 AND to_regprocedure('record_job_metrics()') IS NULL`).Scan(&removed); err != nil || !removed {
		t.Fatal("empty downgrade left metric objects", err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	jobMetricMigrationZero(t, f)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("round-trip schema is not ready", err)
	}
}

func TestJobMetricsMigrationExistingJobsStartAtNewEpoch(t *testing.T) {
	f := newJobFixture(t)
	jobMetricMigration(t, f, "down", 46)
	jobMetricMigration(t, f, "down", 45)
	jobMetricMigration(t, f, "down", 44)
	terminal := f.submit(t, "pre-metrics-terminal")
	if _, err := f.s.CancelJob(f.ctx, f.a, terminal.ID); err != nil {
		t.Fatal(err)
	}
	running := f.submit(t, "pre-metrics-running")
	lease := f.claim(t, "pre-metrics-worker")
	other, err := f.s.RegisterLibrary(f.ctx, "pre-metrics-queued", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queued, replay, err := f.s.SubmitJob(f.ctx, f.a, other.Library.ID, "pre-metrics-queued", domain.JobPriorityManual, f.policy)
	if err != nil || replay {
		t.Fatalf("create legacy queued job: replay=%t error=%v", replay, err)
	}
	beforeJobs := map[string]string{}
	for _, id := range []string{terminal.ID, running.ID, queued.ID} {
		beforeJobs[id] = jobMetricMigrationJob(t, f, id)
	}
	var before, epoch, after time.Time
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT clock_timestamp()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	jobMetricMigrationZero(t, f)
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT started_at,clock_timestamp() FROM job_metric_epoch WHERE singleton`).Scan(&epoch, &after); err != nil {
		t.Fatal(err)
	}
	if epoch.Before(before) || epoch.After(after) {
		t.Fatal("metric epoch did not begin during migration")
	}
	for id, want := range beforeJobs {
		if got := jobMetricMigrationJob(t, f, id); got != want {
			t.Fatal("upgrade rewrote an existing queued, running or terminal job")
		}
	}
	if err := f.s.FinishJob(f.ctx, lease, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal("finish legacy running job:", err)
	}
	snapshot, err := f.s.JobMetrics(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := snapshot.Groups[5]
	if g.Failed != 1 || g.Succeeded != 0 || g.Cancelled != 0 || g.Duration.Count != 1 || g.Wait.Count != 0 ||
		g.Wait.SumSeconds != 0 || g.Queued != 1 || g.Running != 0 || g.ExpiredRunning != 0 {
		t.Fatalf("legacy transition was backfilled or lost: %+v", g)
	}
}

func TestJobMetricsMigrationRetainedSamplesRefuseDowngrade(t *testing.T) {
	f := newJobFixture(t)
	jobMetricMigration(t, f, "down", 46)
	jobMetricMigration(t, f, "down", 45)
	j := f.submit(t, "retained-metrics")
	if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal(err)
	}
	before := jobMetricMigrationStorage(t, f)
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("downgrade discarded retained metrics")
	}
	// golang-migrate records the target version as dirty before executing down.
	// A refused migration preserves the data, but is not a clean schema 45.
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != 44 || !dirty {
		t.Fatalf("refused downgrade status: version=%d dirty=%t error=%v", version, dirty, err)
	}
	var storedVersion int
	var storedDirty bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT version,dirty FROM schema_migrations`).Scan(&storedVersion, &storedDirty); err != nil || storedVersion != 44 || !storedDirty {
		t.Fatal("database did not retain the refused downgrade's dirty state", err)
	}
	if got := jobMetricMigrationStorage(t, f); got != before {
		t.Fatal("refused downgrade changed retained metric data")
	}
	if err := f.s.Ready(f.ctx); err == nil {
		t.Fatal("runtime accepted a dirty refused downgrade")
	}
}

func TestJobMetricsMigrationRejectsInvalidStorage(t *testing.T) {
	f := newJobFixture(t)
	before := jobMetricMigrationStorage(t, f)
	for _, tc := range []struct{ name, query string }{
		{"kind", `UPDATE job_metric_totals SET kind='unknown' WHERE kind='inventory_scan' AND priority='manual'`},
		{"priority", `UPDATE job_metric_totals SET priority='urgent' WHERE kind='inventory_scan' AND priority='manual'`},
		{"measure", `UPDATE job_metric_buckets SET measure='latency' WHERE kind='inventory_scan' AND priority='manual' AND measure='wait' AND bucket_index=0`},
		{"bucket index", `UPDATE job_metric_buckets SET bucket_index=13 WHERE kind='inventory_scan' AND priority='manual' AND measure='wait' AND bucket_index=0`},
		{"finite bucket null", `UPDATE job_metric_buckets SET upper_bound_microseconds=NULL WHERE measure='wait' AND bucket_index=0`},
		{"finite bucket wrong", `UPDATE job_metric_buckets SET upper_bound_microseconds=100001 WHERE measure='wait' AND bucket_index=0`},
		{"duration bucket wrong", `UPDATE job_metric_buckets SET upper_bound_microseconds=100000 WHERE measure='duration' AND bucket_index=0`},
		{"overflow bucket finite", `UPDATE job_metric_buckets SET upper_bound_microseconds=86400000000 WHERE bucket_index=12`},
		{"negative bucket", `UPDATE job_metric_buckets SET bucket_count=-1 WHERE bucket_index=0`},
		{"negative outcome", `UPDATE job_metric_totals SET cancelled_total=-1`},
		{"negative count", `UPDATE job_metric_totals SET wait_count=-1`},
		{"negative sum", `UPDATE job_metric_totals SET wait_count=1,wait_sum_microseconds=-1`},
		{"wait NaN", `UPDATE job_metric_totals SET wait_count=1,wait_sum_microseconds='NaN'`},
		{"wait infinity", `UPDATE job_metric_totals SET wait_count=1,wait_sum_microseconds='Infinity'`},
		{"duration NaN", `UPDATE job_metric_totals SET succeeded_total=1,duration_count=1,duration_sum_microseconds='NaN'`},
		{"duration infinity", `UPDATE job_metric_totals SET succeeded_total=1,duration_count=1,duration_sum_microseconds='-Infinity'`},
		{"sum exceeds numeric range", `UPDATE job_metric_totals SET wait_count=1,wait_sum_microseconds=1000000000000000000000000000000`},
		{"wait sum without sample", `UPDATE job_metric_totals SET wait_sum_microseconds=1`},
		{"duration sum without sample", `UPDATE job_metric_totals SET duration_sum_microseconds=1`},
		{"duration exceeds outcomes", `UPDATE job_metric_totals SET duration_count=1`},
		{"epoch singleton", `UPDATE job_metric_epoch SET singleton=false`},
		{"epoch infinity", `UPDATE job_metric_epoch SET started_at='infinity'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.s.Pool.Exec(f.ctx, tc.query)
			jobMetricMigrationRejected(t, err)
			if got := jobMetricMigrationStorage(t, f); got != before {
				t.Fatal("rejected update changed durable metric rows")
			}
		})
	}
}

func TestJobMetricsMigrationRejectsNonfiniteJobTimestamps(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "invalid-metric-timestamps")
	for _, tc := range []struct{ name, timestamps string }{
		{"created", "created_at='infinity',started_at=clock_timestamp()"},
		{"started", "started_at='infinity'"},
		{"negative started", "started_at='-infinity'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, jobBefore := jobMetricMigrationStorage(t, f), jobMetricMigrationJob(t, f, j.ID)
			_, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='running',owner='timestamp-worker',
			 lease_until=clock_timestamp()+interval '1 minute',`+tc.timestamps+` WHERE id=$1`, j.ID)
			jobMetricMigrationRejected(t, err)
			if jobMetricMigrationStorage(t, f) != before || jobMetricMigrationJob(t, f, j.ID) != jobBefore {
				t.Fatal("nonfinite first start partially committed")
			}
		})
	}
	f.claim(t, "finite-start-worker")
	for _, tc := range []struct{ name, timestamps string }{
		{"finished", "finished_at='infinity'"},
		{"terminal started", "started_at='infinity',finished_at=clock_timestamp()"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, jobBefore := jobMetricMigrationStorage(t, f), jobMetricMigrationJob(t, f, j.ID)
			_, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='failed',owner=NULL,lease_until=NULL,`+tc.timestamps+` WHERE id=$1`, j.ID)
			jobMetricMigrationRejected(t, err)
			if jobMetricMigrationStorage(t, f) != before || jobMetricMigrationJob(t, f, j.ID) != jobBefore {
				t.Fatal("nonfinite finish partially committed")
			}
		})
	}
}

func TestJobMetricsMigrationMissingRowsRollBackTransition(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "missing-metric-row")
	before, jobBefore := jobMetricMigrationStorage(t, f), jobMetricMigrationJob(t, f, j.ID)
	for _, tc := range []struct{ name, remove string }{
		{"epoch", `DELETE FROM job_metric_epoch`},
		{"selected bucket", `DELETE FROM job_metric_buckets WHERE kind='inventory_scan' AND priority='manual' AND measure='wait' AND bucket_index=1`},
		{"totals", `DELETE FROM job_metric_buckets WHERE kind='inventory_scan' AND priority='manual'; DELETE FROM job_metric_totals WHERE kind='inventory_scan' AND priority='manual'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := f.s.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx)
			if _, err := tx.Exec(f.ctx, tc.remove); err != nil {
				t.Fatal("prepare missing metric row:", err)
			}
			_, err = tx.Exec(f.ctx, `UPDATE jobs SET state='running',owner='missing-metric-worker',
			 lease_until=clock_timestamp()+interval '1 minute',created_at='2026-01-01 00:00:00+00',
			 started_at='2026-01-01 00:00:00.5+00' WHERE id=$1`, j.ID)
			jobMetricMigrationRejected(t, err)
			if err := tx.Rollback(f.ctx); err != nil {
				t.Fatal(err)
			}
			if jobMetricMigrationStorage(t, f) != before || jobMetricMigrationJob(t, f, j.ID) != jobBefore {
				t.Fatal("failed metric update did not roll back the entire transition")
			}
		})
	}
}

func TestJobMetricsMigrationTriggerIgnoresCallerSearchPath(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "temp-metric-shadow")
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	var schema string
	if err := tx.QueryRow(f.ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	var invokerAndFixedPath bool
	if err := tx.QueryRow(f.ctx, `SELECT NOT p.prosecdef AND 'search_path=pg_catalog'=ANY(p.proconfig)
	 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
	 WHERE n.nspname=$1 AND p.proname='record_job_metrics' AND p.pronargs=0`, schema).Scan(&invokerAndFixedPath); err != nil || !invokerAndFixedPath {
		t.Fatal("metric trigger is not an invoker with a fixed lookup path", err)
	}
	for _, table := range []string{"job_metric_epoch", "job_metric_totals", "job_metric_buckets"} {
		query := "CREATE TEMP TABLE " + pgx.Identifier{table}.Sanitize() + " ON COMMIT DROP AS SELECT * FROM " + pgx.Identifier{schema, table}.Sanitize()
		if _, err := tx.Exec(f.ctx, query); err != nil {
			t.Fatal("prepare shadow metric table:", err)
		}
	}
	if _, err := tx.Exec(f.ctx, `SELECT set_config('search_path',$1,true)`, "pg_temp,"+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `UPDATE `+pgx.Identifier{schema, "jobs"}.Sanitize()+` SET state='running',owner='shadow-worker',
	 lease_until=clock_timestamp()+interval '1 minute',created_at='2026-01-01 00:00:00+00',
	 started_at='2026-01-01 00:00:00.5+00' WHERE id=$1`, j.ID); err != nil {
		t.Fatal("caller search_path prevented metric update:", err)
	}
	var realCount, shadowCount, realBucket, shadowBuckets int64
	query := `SELECT (SELECT sum(wait_count)::bigint FROM ` + pgx.Identifier{schema, "job_metric_totals"}.Sanitize() + `),
	 (SELECT sum(wait_count)::bigint FROM pg_temp.job_metric_totals),
	 (SELECT bucket_count FROM ` + pgx.Identifier{schema, "job_metric_buckets"}.Sanitize() + `
	  WHERE kind='inventory_scan' AND priority='manual' AND measure='wait' AND bucket_index=1),
	 (SELECT sum(bucket_count)::bigint FROM pg_temp.job_metric_buckets)`
	if err := tx.QueryRow(f.ctx, query).Scan(&realCount, &shadowCount, &realBucket, &shadowBuckets); err != nil {
		t.Fatal(err)
	}
	if realCount != 1 || realBucket != 1 || shadowCount != 0 || shadowBuckets != 0 {
		t.Fatalf("caller redirected trigger: real=%d/%d shadow=%d/%d", realCount, realBucket, shadowCount, shadowBuckets)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.s.JobMetrics(f.ctx)
	if err != nil || snapshot.Groups[5].Wait.Count != 1 || snapshot.Groups[5].Wait.SumSeconds != .5 {
		t.Fatal("qualified metrics did not survive commit", err)
	}
}

func TestJobMetricsMigrationCountersSurviveLibraryDeletion(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "deleted-library-metrics")
	if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal(err)
	}
	var foreignKeys int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM pg_constraint
	 WHERE contype='f' AND conrelid IN ('job_metric_epoch'::regclass,'job_metric_totals'::regclass,'job_metric_buckets'::regclass)
	 AND confrelid IN ('jobs'::regclass,'libraries'::regclass,'library_roots'::regclass)`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatal("metric lifetime depends on jobs or libraries", err)
	}
	before := jobMetricMigrationStorage(t, f)
	deleteJobMetricsFixtureLibrary(t, f)
	var jobs int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs WHERE id=$1`, j.ID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("fixture cleanup retained a job", err)
	}
	if jobMetricMigrationStorage(t, f) != before {
		t.Fatal("library/job deletion reduced durable counters")
	}
}
