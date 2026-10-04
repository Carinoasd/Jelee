BEGIN;
-- G50.3 data consistency checker. A check reads the catalog, the last
-- accepted inventory baseline and the derived tables and reports where they
-- disagree. It never deletes; the few safe repairs it can apply are journaled
-- so they can be reverted, and every repair batch is audited.
--
-- No state transition may commit while the job kind and metric dimensions
-- change; the metric trigger requires a totals row for every claimable kind.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets IN ACCESS EXCLUSIVE MODE;

ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed','consistency_check_failed'));
ALTER TABLE jobs ADD CONSTRAINT jobs_consistency_check_error_check CHECK(kind='consistency_check' OR error_code<>'consistency_check_failed');

ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check'));
INSERT INTO job_metric_totals(kind,priority) VALUES('consistency_check','background'),('consistency_check','manual');
INSERT INTO job_metric_buckets(kind,priority,measure,bucket_index,upper_bound_microseconds)
 SELECT 'consistency_check',priority,measure,bucket_index,upper_bound_microseconds
 FROM job_metric_buckets WHERE kind='inventory_scan';

-- One row per check run. library_id is NULL when a command-line run covered
-- every library. The report is the stable JSON document of
-- docs/consistency.md; it holds identifiers and root-relative paths only as
-- the logging path mode allows, never an absolute path.
CREATE TABLE consistency_runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 job_id uuid CONSTRAINT consistency_runs_job_key UNIQUE REFERENCES jobs(id) ON DELETE SET NULL,
 library_id uuid REFERENCES libraries(id) ON DELETE CASCADE,
 origin text NOT NULL CONSTRAINT consistency_runs_origin_check CHECK(origin IN ('cli','job')),
 mode text NOT NULL CONSTRAINT consistency_runs_mode_check CHECK(mode IN ('report','fix')),
 state text NOT NULL DEFAULT 'running' CONSTRAINT consistency_runs_state_check CHECK(state IN ('running','completed','partial','cancelled','failed')),
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 findings bigint NOT NULL DEFAULT 0 CONSTRAINT consistency_runs_findings_check CHECK(findings>=0),
 fixed bigint NOT NULL DEFAULT 0 CONSTRAINT consistency_runs_fixed_check CHECK(fixed>=0),
 report jsonb CONSTRAINT consistency_runs_report_check CHECK(jsonb_typeof(report)='object' AND octet_length(report::text)<=4194304),
 CONSTRAINT consistency_runs_finished_check CHECK((state='running')=(finished_at IS NULL)),
 CONSTRAINT consistency_runs_report_state_check CHECK(state='running' OR report IS NOT NULL),
 CONSTRAINT consistency_runs_job_origin_check CHECK(origin='job' OR job_id IS NULL)
);
CREATE INDEX consistency_runs_finished_idx ON consistency_runs(finished_at DESC,id DESC) WHERE finished_at IS NOT NULL;
CREATE INDEX consistency_runs_library_idx ON consistency_runs(library_id,started_at DESC);

-- Per run, library and check counts. The metrics snapshot reads the latest
-- finished row of every library and check from here instead of parsing
-- reports. library_id is NULL for database-wide checks.
CREATE TABLE consistency_run_checks (
 run_id uuid NOT NULL REFERENCES consistency_runs(id) ON DELETE CASCADE,
 library_id uuid REFERENCES libraries(id) ON DELETE CASCADE,
 check_id text NOT NULL CONSTRAINT consistency_run_checks_check_check CHECK(check_id IN ('orphan_item','orphan_file','version_count','watch_stats_drift','image_file','image_variant_index','nfo_state','sidecar_file','probe_cache_stale','constraint_state')),
 status text NOT NULL CONSTRAINT consistency_run_checks_status_check CHECK(status IN ('ok','findings','skipped','incomplete')),
 examined bigint NOT NULL CONSTRAINT consistency_run_checks_examined_check CHECK(examined>=0),
 findings bigint NOT NULL CONSTRAINT consistency_run_checks_findings_check CHECK(findings>=0),
 fixed bigint NOT NULL DEFAULT 0 CONSTRAINT consistency_run_checks_fixed_check CHECK(fixed>=0)
);
CREATE UNIQUE INDEX consistency_run_checks_key ON consistency_run_checks(run_id,COALESCE(library_id,'00000000-0000-0000-0000-000000000000'::uuid),check_id);
CREATE INDEX consistency_run_checks_latest_idx ON consistency_run_checks(library_id,check_id);

-- Every applied repair with the exact values before and after it, so
-- `jelee-cli consistency revert` can restore them while the row still holds
-- the repaired value. Journaled runs are never trimmed.
CREATE TABLE consistency_fix_journal (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 run_id uuid NOT NULL REFERENCES consistency_runs(id) ON DELETE RESTRICT,
 fix text NOT NULL CONSTRAINT consistency_fix_journal_fix_check CHECK(fix IN ('user_item_data.last_source_id','playback_sessions.source_id','watch_stats_daily.counters')),
 target jsonb NOT NULL CONSTRAINT consistency_fix_journal_target_check CHECK(jsonb_typeof(target)='object' AND octet_length(target::text)<=1024),
 before_state jsonb NOT NULL CONSTRAINT consistency_fix_journal_before_check CHECK(jsonb_typeof(before_state)='object' AND octet_length(before_state::text)<=1024),
 after_state jsonb NOT NULL CONSTRAINT consistency_fix_journal_after_check CHECK(jsonb_typeof(after_state)='object' AND octet_length(after_state::text)<=1024),
 applied_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 reverted_at timestamptz,
 CONSTRAINT consistency_fix_journal_revert_check CHECK(reverted_at IS NULL OR reverted_at>=applied_at)
);
CREATE INDEX consistency_fix_journal_run_idx ON consistency_fix_journal(run_id,id);

-- The dead-letter gauge counts these rows on every metrics scrape.
CREATE INDEX webhook_deliveries_dead_idx ON webhook_deliveries(webhook_id) WHERE state='dead';
COMMIT;
