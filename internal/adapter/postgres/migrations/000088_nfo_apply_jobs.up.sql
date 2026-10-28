BEGIN;
-- G39.14 batch NFO apply (first of the three full-library operations). One
-- nfo_apply job applies the NFO fields of every item of one library through
-- the per-item apply path; nfo_apply_job_state is its resume point and its
-- counters.
--
-- No state transition may commit while the job kind and metric dimensions
-- change; the metric trigger requires a totals row for every claimable kind.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets IN ACCESS EXCLUSIVE MODE;

ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed','consistency_check_failed','nfo_apply_failed'));
ALTER TABLE jobs ADD CONSTRAINT jobs_nfo_apply_error_check CHECK(kind='nfo_apply' OR error_code<>'nfo_apply_failed');

ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply'));
INSERT INTO job_metric_totals(kind,priority) VALUES('nfo_apply','background'),('nfo_apply','manual');
INSERT INTO job_metric_buckets(kind,priority,measure,bucket_index,upper_bound_microseconds)
 SELECT 'nfo_apply',priority,measure,bucket_index,upper_bound_microseconds
 FROM job_metric_buckets WHERE kind='inventory_scan';

-- One row per job. cursor_item_id is the last item the job finished with;
-- items are visited in ascending ID order, so a replacement lease continues
-- after it. The counters are committed together with the cursor.
CREATE TABLE nfo_apply_job_state (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 library_id uuid NOT NULL,
 cursor_item_id uuid,
 applied integer NOT NULL DEFAULT 0 CONSTRAINT nfo_apply_job_state_applied_check CHECK(applied>=0),
 unchanged integer NOT NULL DEFAULT 0 CONSTRAINT nfo_apply_job_state_unchanged_check CHECK(unchanged>=0),
 skipped integer NOT NULL DEFAULT 0 CONSTRAINT nfo_apply_job_state_skipped_check CHECK(skipped>=0),
 failed integer NOT NULL DEFAULT 0 CONSTRAINT nfo_apply_job_state_failed_check CHECK(failed>=0),
 updated_at timestamptz NOT NULL
);
COMMIT;
