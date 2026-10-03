BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE nfo_write_requests,nfo_write_entries,job_metric_totals,job_metric_buckets IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM jobs WHERE kind='nfo_write' OR error_code='nfo_write_failed')
  OR EXISTS(SELECT 1 FROM nfo_write_requests)
  OR EXISTS(SELECT 1 FROM nfo_write_entries)
  OR EXISTS(SELECT 1 FROM job_metric_totals WHERE kind='nfo_write' AND (succeeded_total<>0 OR failed_total<>0 OR cancelled_total<>0 OR wait_count<>0 OR wait_sum_microseconds<>0 OR duration_count<>0 OR duration_sum_microseconds<>0))
  OR EXISTS(SELECT 1 FROM job_metric_buckets WHERE kind='nfo_write' AND bucket_count<>0) THEN
  RAISE EXCEPTION 'retain nfo write jobs and metrics before downgrade';
 END IF;
END $$;
DROP TRIGGER verify_nfo_write_job ON jobs;
DROP TABLE nfo_write_entries;
DROP TABLE nfo_write_requests;
DROP FUNCTION verify_nfo_write_job();
DROP FUNCTION guard_nfo_write_intent();
DELETE FROM job_metric_buckets WHERE kind='nfo_write';
DELETE FROM job_metric_totals WHERE kind='nfo_write';
ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import'));
ALTER TABLE jobs DROP CONSTRAINT jobs_nfo_write_error_check;
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed'));
COMMIT;
