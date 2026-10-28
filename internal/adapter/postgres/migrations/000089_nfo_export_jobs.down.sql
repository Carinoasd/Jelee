BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets,nfo_export_job_state IN ACCESS EXCLUSIVE MODE;
-- Export jobs, their progress and their child write jobs are retained state:
-- an older binary could neither resume nor display them, and the older
-- one-active-job index would reject a running pair.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM jobs WHERE kind='nfo_export' OR error_code='nfo_export_failed' OR export_job_id IS NOT NULL)
  OR EXISTS(SELECT 1 FROM nfo_export_job_state)
  OR EXISTS(SELECT 1 FROM job_metric_totals WHERE kind='nfo_export' AND (succeeded_total<>0 OR failed_total<>0 OR cancelled_total<>0 OR wait_count<>0 OR wait_sum_microseconds<>0 OR duration_count<>0 OR duration_sum_microseconds<>0))
  OR EXISTS(SELECT 1 FROM job_metric_buckets WHERE kind='nfo_export' AND bucket_count<>0) THEN
  RAISE EXCEPTION 'retained nfo export state prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE nfo_export_job_state;
DROP INDEX jobs_active_library_idx;
CREATE UNIQUE INDEX jobs_active_library_idx ON jobs(library_id) WHERE state IN ('queued','running');
ALTER TABLE jobs DROP CONSTRAINT jobs_export_job_kind_check;
ALTER TABLE jobs DROP COLUMN export_job_id;
DELETE FROM job_metric_buckets WHERE kind='nfo_export';
DELETE FROM job_metric_totals WHERE kind='nfo_export';
ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply'));
ALTER TABLE jobs DROP CONSTRAINT jobs_nfo_export_error_check;
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed','consistency_check_failed','nfo_apply_failed'));
COMMIT;
