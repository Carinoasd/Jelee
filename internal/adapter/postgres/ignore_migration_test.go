package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Compare fields shared with schema 7. Later additive columns have dedicated
// migration tests; every preexisting parent field remains in this projection.
func ignoreLegacySnapshot(t *testing.T, f jobFixture) string {
	t.Helper()
	var result string
	err := f.s.Pool.QueryRow(f.ctx, `SELECT jsonb_build_object(
 'jobs',(SELECT jsonb_agg(to_jsonb(j)-'ignore_requested' ORDER BY id) FROM jobs j),
 'libraries',(SELECT jsonb_agg(to_jsonb(l)-'inventory_baseline_revision'-'metadata_language'-'metadata_preferences_revision'-'metadata_image_languages' ORDER BY id) FROM libraries l),
 'roots',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM library_roots r),
 'inventory',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM job_inventory i),
 'directories',(SELECT jsonb_agg(to_jsonb(d) ORDER BY job_id,root_id,path) FROM job_directories d),
 'baseline',(SELECT jsonb_agg(to_jsonb(b)-'observed_revision' ORDER BY library_id,root_id,path) FROM library_inventory_baseline b),
 'nfo_cache',(SELECT jsonb_agg(to_jsonb(c) ORDER BY root_id,relative_path) FROM nfo_cache c),
 'nfo_phases',(SELECT jsonb_agg(to_jsonb(p) ORDER BY job_id) FROM nfo_job_state p),
 'nfo_requests',(SELECT jsonb_agg(to_jsonb(r) ORDER BY job_id) FROM nfo_job_requests r),
 'images',(SELECT jsonb_agg(to_jsonb(i) ORDER BY job_id) FROM image_job_state i),
 'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM audit_logs a))::text`).Scan(&result)
	if err != nil {
		t.Fatal("read private legacy migration snapshot")
	}
	return result
}
func ignoreIntentSnapshot(t *testing.T, f jobFixture) string {
	t.Helper()
	var result string
	err := f.s.Pool.QueryRow(f.ctx, `SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs j),'requests',(SELECT jsonb_agg(to_jsonb(r) ORDER BY job_id) FROM job_ignore_requests r))::text`).Scan(&result)
	if err != nil {
		t.Fatal("read private ignore migration snapshot")
	}
	return result
}
func denyIgnoreDowngrade(t *testing.T, f jobFixture) {
	t.Helper()
	before := ignoreIntentSnapshot(t, f)
	legacy := ignoreLegacySnapshot(t, f)
	body, err := migrationFiles.ReadFile("migrations/000008_ignore_intent.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.s.Pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal("acquire migration fixture connection")
	}
	_, denied := c.Exec(f.ctx, string(body))
	_, rollback := c.Exec(f.ctx, "ROLLBACK")
	c.Release()
	var pgerr *pgconn.PgError
	if !errors.As(denied, &pgerr) || pgerr.Code != "55000" || pgerr.Message != "retained ignore intent prevents rollback" || rollback != nil {
		t.Fatal("ignore downgrade did not fail through its intentional guard")
	}
	if before != ignoreIntentSnapshot(t, f) || legacy != ignoreLegacySnapshot(t, f) {
		t.Fatal("refused downgrade changed retained intent or existing data")
	}
	v, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || dirty || v != SchemaVersion {
		t.Fatal("direct guard regression dirtied migration version")
	}
}
func submitIgnoreForMigration(t *testing.T, f jobFixture) domain.Job {
	t.Helper()
	intent := domain.ScanIntent{Ignore: domain.IgnoreIntent{Mode: domain.IgnoreModeJeleeignore, CaseMode: domain.IgnoreCaseSensitive}}
	j, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "retained-ignore", domain.JobPriorityManual, intent, f.policy, nil, nil)
	if err != nil || replay {
		t.Fatal("create retained ignore migration fixture")
	}
	return j
}

func TestIgnoreMigrationLegacySevenRoundTripAndReadiness(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "existing-cache", "retained.nfo")
	f.parseHead(t, l, nfoValidSummary())
	f.finish(t, l)
	nfoMigrateVersion(t, f.jobFixture, "down", 37)
	nfoMigrateVersion(t, f.jobFixture, "down", 36)
	nfoMigrateVersion(t, f.jobFixture, "down", 35)
	nfoMigrateVersion(t, f.jobFixture, "down", 34)
	nfoMigrateVersion(t, f.jobFixture, "down", 33)
	nfoMigrateVersion(t, f.jobFixture, "down", 32)
	nfoMigrateVersion(t, f.jobFixture, "down", 31)
	nfoMigrateVersion(t, f.jobFixture, "down", 30)
	nfoMigrateVersion(t, f.jobFixture, "down", 29)
	nfoMigrateVersion(t, f.jobFixture, "down", 28)
	nfoMigrateVersion(t, f.jobFixture, "down", 27)
	nfoMigrateVersion(t, f.jobFixture, "down", 26)
	nfoMigrateVersion(t, f.jobFixture, "down", 25)
	nfoMigrateVersion(t, f.jobFixture, "down", 24)
	nfoMigrateVersion(t, f.jobFixture, "down", 23)
	nfoMigrateVersion(t, f.jobFixture, "down", 22)
	nfoMigrateVersion(t, f.jobFixture, "down", 21)
	nfoMigrateVersion(t, f.jobFixture, "down", 20)
	nfoMigrateVersion(t, f.jobFixture, "down", 19)
	nfoMigrateVersion(t, f.jobFixture, "down", 18)
	nfoMigrateVersion(t, f.jobFixture, "down", 17)
	nfoMigrateVersion(t, f.jobFixture, "down", 16)
	nfoMigrateVersion(t, f.jobFixture, "down", 15)
	nfoMigrateVersion(t, f.jobFixture, "down", 14)
	nfoMigrateVersion(t, f.jobFixture, "down", 13)
	nfoMigrateVersion(t, f.jobFixture, "down", 12)
	nfoMigrateVersion(t, f.jobFixture, "down", 11)
	nfoMigrateVersion(t, f.jobFixture, "down", 10)
	nfoMigrateVersion(t, f.jobFixture, "down", 9)
	nfoMigrateVersion(t, f.jobFixture, "down", 8)
	nfoMigrateVersion(t, f.jobFixture, "down", 7)
	// Construct a genuine pre-008 queued job while the marker column does not
	// exist. Current repository admission intentionally requires schema 8.
	var legacyID string
	err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,priority,directory_total,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,inventory_generation) SELECT id,$2::uuid,'legacy-schema-seven','manual',1,1,20,100,100,3,100,100,inventory_generation FROM libraries WHERE id=$1::uuid RETURNING jobs.id::text`, f.registration.Library.ID, f.a.UserID).Scan(&legacyID)
	if err != nil {
		t.Fatal("create genuine schema-seven queued job")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO job_directories(job_id,root_id,path) VALUES($1::uuid,$2::uuid,'.')`, legacyID, f.registration.RootID); err != nil {
		t.Fatal("create legacy inventory frontier")
	}
	before := ignoreLegacySnapshot(t, f.jobFixture)
	if f.s.Ready(f.ctx) == nil {
		t.Fatal("schema7 was accepted by schema8 binary")
	}
	if old, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2); err == nil || old != nil {
		if old != nil {
			old.Pool.Close()
		}
		t.Fatal("Open admitted old schema")
	}
	nfoMigrateVersion(t, f.jobFixture, "up", SchemaVersion)
	if before != ignoreLegacySnapshot(t, f.jobFixture) {
		t.Fatal("008 upgrade modified preexisting data")
	}
	var enabled, requests int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM jobs WHERE ignore_requested),(SELECT count(*) FROM job_ignore_requests)`).Scan(&enabled, &requests); err != nil || enabled != 0 || requests != 0 {
		t.Fatal("upgrade inferred ignore intent for old jobs")
	}
	if f.s.Ready(f.ctx) != nil {
		t.Fatal("clean current schema failed readiness")
	}
	for _, state := range []struct {
		version int
		dirty   bool
	}{{SchemaVersion, true}, {SchemaVersion + 1, false}} {
		if _, err = f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=$2`, state.version, state.dirty); err != nil {
			t.Fatal("set private readiness fixture")
		}
		if f.s.Ready(f.ctx) == nil {
			t.Fatal("dirty/future schema admitted")
		}
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, SchemaVersion); err != nil {
		t.Fatal("restore private readiness fixture")
	}
	nfoMigrateVersion(t, f.jobFixture, "down", 37)
	nfoMigrateVersion(t, f.jobFixture, "down", 36)
	nfoMigrateVersion(t, f.jobFixture, "down", 35)
	nfoMigrateVersion(t, f.jobFixture, "down", 34)
	nfoMigrateVersion(t, f.jobFixture, "down", 33)
	nfoMigrateVersion(t, f.jobFixture, "down", 32)
	nfoMigrateVersion(t, f.jobFixture, "down", 31)
	nfoMigrateVersion(t, f.jobFixture, "down", 30)
	nfoMigrateVersion(t, f.jobFixture, "down", 29)
	nfoMigrateVersion(t, f.jobFixture, "down", 28)
	nfoMigrateVersion(t, f.jobFixture, "down", 27)
	nfoMigrateVersion(t, f.jobFixture, "down", 26)
	nfoMigrateVersion(t, f.jobFixture, "down", 25)
	nfoMigrateVersion(t, f.jobFixture, "down", 24)
	nfoMigrateVersion(t, f.jobFixture, "down", 23)
	nfoMigrateVersion(t, f.jobFixture, "down", 22)
	nfoMigrateVersion(t, f.jobFixture, "down", 21)
	nfoMigrateVersion(t, f.jobFixture, "down", 20)
	nfoMigrateVersion(t, f.jobFixture, "down", 19)
	nfoMigrateVersion(t, f.jobFixture, "down", 18)
	nfoMigrateVersion(t, f.jobFixture, "down", 17)
	nfoMigrateVersion(t, f.jobFixture, "down", 16)
	nfoMigrateVersion(t, f.jobFixture, "down", 15)
	nfoMigrateVersion(t, f.jobFixture, "down", 14)
	nfoMigrateVersion(t, f.jobFixture, "down", 13)
	nfoMigrateVersion(t, f.jobFixture, "down", 12)
	nfoMigrateVersion(t, f.jobFixture, "down", 11)
	nfoMigrateVersion(t, f.jobFixture, "down", 10)
	nfoMigrateVersion(t, f.jobFixture, "down", 9)
	nfoMigrateVersion(t, f.jobFixture, "down", 8)
	nfoMigrateVersion(t, f.jobFixture, "down", 7)
	if before != ignoreLegacySnapshot(t, f.jobFixture) {
		t.Fatal("008 off-only downgrade changed legacy data")
	}
	nfoMigrateVersion(t, f.jobFixture, "up", SchemaVersion)
	if before != ignoreLegacySnapshot(t, f.jobFixture) {
		t.Fatal("008 repeated upgrade changed legacy data")
	}
	claimed, err := f.s.ClaimJob(f.ctx, "legacy-off-worker", false, time.Minute)
	if err != nil || claimed.Job.ID != legacyID {
		t.Fatal("old off job became unclaimable")
	}
	if _, err = f.s.NextScanDirectory(f.ctx, claimed); err != nil {
		t.Fatal("old off job lost its inventory frontier")
	}
	if err = f.s.FinishJob(f.ctx, claimed, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal("old off job cannot terminate")
	}
}

func TestIgnoreMigrationDownRefusesEveryRetainedJobState(t *testing.T) {
	for _, state := range []string{domain.JobQueued, domain.JobRunning, domain.JobSucceeded, domain.JobFailed, domain.JobCancelled} {
		t.Run(state, func(t *testing.T) {
			f := newJobFixture(t)
			j := submitIgnoreForMigration(t, f)
			switch state {
			case domain.JobRunning:
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='running',owner='migration-fixture',lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1::uuid`, j.ID); err != nil {
					t.Fatal("prepare stored active lease")
				}
			case domain.JobSucceeded, domain.JobFailed, domain.JobCancelled:
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state=$2,finished_at=clock_timestamp() WHERE id=$1::uuid`, j.ID, state); err != nil {
					t.Fatal("prepare retained terminal history")
				}
			}
			denyIgnoreDowngrade(t, f)
		})
	}
}

func TestIgnoreMigrationDownRefusesIncompleteRetainedIntent(t *testing.T) {
	for _, shape := range []string{"marker-only", "row-only", "orphan-row"} {
		t.Run(shape, func(t *testing.T) {
			f := newJobFixture(t)
			j := submitIgnoreForMigration(t, f)
			switch shape {
			case "marker-only":
				if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM job_ignore_requests WHERE job_id=$1::uuid`, j.ID); err != nil {
					t.Fatal("remove private request fixture")
				}
			case "row-only":
				if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs DISABLE TRIGGER job_ignore_requested_immutable`); err != nil {
					t.Fatal("disable private marker protection")
				}
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET ignore_requested=false WHERE id=$1::uuid`, j.ID); err != nil {
					t.Fatal("create inconsistent private marker")
				}
			case "orphan-row":
				// Remove this schema's exact parent FK to model damaged stored data,
				// without disabling server-wide constraints or changing another schema.
				var constraint string
				if err := f.s.Pool.QueryRow(f.ctx, `SELECT conname FROM pg_constraint WHERE conrelid='job_ignore_requests'::regclass AND confrelid='jobs'::regclass AND contype='f'`).Scan(&constraint); err != nil {
					t.Fatal("find private parent constraint")
				}
				if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE job_ignore_requests DROP CONSTRAINT `+pgx.Identifier{constraint}.Sanitize()); err != nil {
					t.Fatal("remove private parent constraint")
				}
				if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, j.ID); err != nil {
					t.Fatal("create orphan request fixture")
				}
			}
			denyIgnoreDowngrade(t, f)
		})
	}
}

func TestIgnoreMigrationHistoryTrimAllowsDowngrade(t *testing.T) {
	f := newJobFixture(t)
	j := submitIgnoreForMigration(t, f)
	if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal("terminate enabled queued fixture")
	}
	denyIgnoreDowngrade(t, f)
	f.policy.HistoryLimit = 1
	f.complete(t, "newer-off-history", []string{"retained.mkv"}, 0)
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM jobs WHERE ignore_requested)+(SELECT count(*) FROM job_ignore_requests)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("ordinary terminal history trim did not remove request with parent")
	}
	before := ignoreLegacySnapshot(t, f)
	nfoMigrateVersion(t, f, "down", 37)
	nfoMigrateVersion(t, f, "down", 36)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "down", 31)
	nfoMigrateVersion(t, f, "down", 30)
	nfoMigrateVersion(t, f, "down", 29)
	nfoMigrateVersion(t, f, "down", 28)
	nfoMigrateVersion(t, f, "down", 27)
	nfoMigrateVersion(t, f, "down", 26)
	nfoMigrateVersion(t, f, "down", 25)
	nfoMigrateVersion(t, f, "down", 24)
	nfoMigrateVersion(t, f, "down", 23)
	nfoMigrateVersion(t, f, "down", 22)
	nfoMigrateVersion(t, f, "down", 21)
	nfoMigrateVersion(t, f, "down", 20)
	nfoMigrateVersion(t, f, "down", 19)
	nfoMigrateVersion(t, f, "down", 18)
	nfoMigrateVersion(t, f, "down", 17)
	nfoMigrateVersion(t, f, "down", 16)
	nfoMigrateVersion(t, f, "down", 15)
	nfoMigrateVersion(t, f, "down", 14)
	nfoMigrateVersion(t, f, "down", 13)
	nfoMigrateVersion(t, f, "down", 12)
	nfoMigrateVersion(t, f, "down", 11)
	nfoMigrateVersion(t, f, "down", 10)
	nfoMigrateVersion(t, f, "down", 9)
	nfoMigrateVersion(t, f, "down", 8)
	nfoMigrateVersion(t, f, "down", 7)
	if before != ignoreLegacySnapshot(t, f) {
		t.Fatal("downgrade after trim changed off job baseline")
	}
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	if before != ignoreLegacySnapshot(t, f) {
		t.Fatal("re-upgrade after trim changed off job baseline")
	}
}

func TestIgnoreMigrationDatabaseConstraints(t *testing.T) {
	f := newJobFixture(t)
	enabled := submitIgnoreForMigration(t, f)
	other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
	if err != nil {
		t.Fatal("create second private library")
	}
	off, replay, err := f.s.SubmitJob(f.ctx, f.a, other.Library.ID, "off", domain.JobPriorityManual, f.policy)
	if err != nil || replay {
		t.Fatal("create off marker fixture")
	}
	before := ignoreIntentSnapshot(t, f)
	for _, test := range []struct {
		name, query string
		args        []any
	}{
		{"enabled-marker", `UPDATE jobs SET ignore_requested=false WHERE id=$1::uuid`, []any{enabled.ID}},
		{"off-marker", `UPDATE jobs SET ignore_requested=true WHERE id=$1::uuid`, []any{off.ID}},
		{"case-is-frozen", `UPDATE job_ignore_requests SET case_mode='ascii-insensitive' WHERE job_id=$1::uuid`, []any{enabled.ID}},
		{"library-is-frozen", `UPDATE job_ignore_requests SET library_id=$2::uuid WHERE job_id=$1::uuid`, []any{enabled.ID, other.Library.ID}},
		{"off-request", `INSERT INTO job_ignore_requests(job_id,library_id,case_mode,program_version,proof_version) VALUES($1::uuid,$2::uuid,'sensitive','jeleeignore-v1','jeleeignore-proof-v1')`, []any{off.ID, other.Library.ID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := f.s.Pool.Exec(f.ctx, test.query, test.args...)
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
				t.Fatal("database did not enforce immutable retained intent")
			}
			if before != ignoreIntentSnapshot(t, f) {
				t.Fatal("rejected mutation changed retained intent")
			}
		})
	}
	// Disabling only our user trigger inside a rolled-back private transaction
	// proves CHECK/FK constraints themselves, rather than an earlier trigger.
	for _, test := range []struct {
		name, query, code string
		args              []any
	}{
		{"mode-check", `UPDATE job_ignore_requests SET mode='other' WHERE job_id=$1::uuid`, "23514", []any{enabled.ID}},
		{"case-check", `UPDATE job_ignore_requests SET case_mode='unicode' WHERE job_id=$1::uuid`, "23514", []any{enabled.ID}},
		{"program-check", `UPDATE job_ignore_requests SET program_version='future' WHERE job_id=$1::uuid`, "23514", []any{enabled.ID}},
		{"proof-check", `UPDATE job_ignore_requests SET proof_version='future' WHERE job_id=$1::uuid`, "23514", []any{enabled.ID}},
		{"required-mode", `UPDATE job_ignore_requests SET mode=NULL WHERE job_id=$1::uuid`, "23502", []any{enabled.ID}},
		{"composite-library", `UPDATE job_ignore_requests SET library_id=$2::uuid WHERE job_id=$1::uuid`, "23503", []any{enabled.ID, other.Library.ID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := f.s.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal("begin constraint fixture")
			}
			defer tx.Rollback(f.ctx)
			if _, err = tx.Exec(f.ctx, `ALTER TABLE job_ignore_requests DISABLE TRIGGER job_ignore_request_immutable`); err != nil {
				t.Fatal("isolate private CHECK/FK validation")
			}
			_, err = tx.Exec(f.ctx, test.query, test.args...)
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != test.code {
				t.Fatal("database CHECK/FK admitted invalid request shape")
			}
			if err = tx.Rollback(f.ctx); err != nil {
				t.Fatal("restore private user trigger")
			}
			if before != ignoreIntentSnapshot(t, f) {
				t.Fatal("constraint failure changed retained state")
			}
		})
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_requests SET case_mode=case_mode WHERE job_id=$1::uuid`, enabled.ID); err != nil {
		t.Fatal("unchanged retained request was rejected")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET ignore_requested=ignore_requested WHERE id=$1::uuid`, enabled.ID); err != nil {
		t.Fatal("unchanged retained marker was rejected")
	}
	if before != ignoreIntentSnapshot(t, f) {
		t.Fatal("no-op validation changed retained intent")
	}
}
