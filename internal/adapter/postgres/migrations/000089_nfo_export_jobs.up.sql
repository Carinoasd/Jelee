BEGIN;
-- G39.14 batch NFO export (second of the three full-library operations). One
-- nfo_export job writes the database metadata of every item of one library
-- into the existing NFO files, only through child nfo_write jobs. The export
-- job never holds a worker while its child runs: nfo_export_job_state is both
-- its resume point and the gate that keeps it unclaimable until the child is
-- terminal.
--
-- No state transition may commit while the job kind and metric dimensions
-- change; the metric trigger requires a totals row for every claimable kind.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets IN ACCESS EXCLUSIVE MODE;

ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed','consistency_check_failed','nfo_apply_failed','nfo_export_failed'));
ALTER TABLE jobs ADD CONSTRAINT jobs_nfo_export_error_check CHECK(kind='nfo_export' OR error_code<>'nfo_export_failed');

ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export'));
INSERT INTO job_metric_totals(kind,priority) VALUES('nfo_export','background'),('nfo_export','manual');
INSERT INTO job_metric_buckets(kind,priority,measure,bucket_index,upper_bound_microseconds)
 SELECT 'nfo_export',priority,measure,bucket_index,upper_bound_microseconds
 FROM job_metric_buckets WHERE kind='inventory_scan';

-- A child nfo_write job of an export names its export job in export_job_id
-- (parent_id already records retries). A library may now have one active
-- top-level job and one active child; the export job and its child are the
-- only pair that coexist. The partial predicate stays the one the job
-- metrics query relies on.
ALTER TABLE jobs ADD COLUMN export_job_id uuid;
ALTER TABLE jobs ADD CONSTRAINT jobs_export_job_kind_check CHECK(export_job_id IS NULL OR kind='nfo_write');
DROP INDEX jobs_active_library_idx;
CREATE UNIQUE INDEX jobs_active_library_idx ON jobs(library_id,(export_job_id IS NOT NULL)) WHERE state IN ('queued','running');

-- One row per job. cursor_item_id is the last item the job finished with;
-- items are visited in ascending ID order. chunk counts the child jobs
-- submitted. child_job_id is the child whose outcome is not yet in the
-- counters, with child_entries prepared entries; the cursor, the child and
-- the counters change together, in one transaction under the export lease.
-- The export job is not claimable while the child is queued or running.
CREATE TABLE nfo_export_job_state (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 library_id uuid NOT NULL,
 cursor_item_id uuid,
 chunk integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_chunk_check CHECK(chunk>=0),
 child_job_id uuid,
 child_entries integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_entries_check CHECK(child_entries BETWEEN 0 AND 100),
 written integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_written_check CHECK(written>=0),
 unchanged integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_unchanged_check CHECK(unchanged>=0),
 skipped_missing integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_skipped_missing_check CHECK(skipped_missing>=0),
 skipped_invalid integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_skipped_invalid_check CHECK(skipped_invalid>=0),
 skipped_other integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_skipped_other_check CHECK(skipped_other>=0),
 failed integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_failed_check CHECK(failed>=0),
 updated_at timestamptz NOT NULL,
 CONSTRAINT nfo_export_job_state_child_check CHECK((child_job_id IS NULL)=(child_entries=0))
);
COMMIT;
