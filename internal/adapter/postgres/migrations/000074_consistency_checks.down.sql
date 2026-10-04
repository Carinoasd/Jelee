BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets,consistency_runs,consistency_run_checks,consistency_fix_journal IN ACCESS EXCLUSIVE MODE;
-- Check reports, repair journals and consistency jobs are retained state:
-- an older binary could neither read nor revert them.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM jobs WHERE kind='consistency_check' OR error_code='consistency_check_failed')
  OR EXISTS(SELECT 1 FROM consistency_runs)
  OR EXISTS(SELECT 1 FROM consistency_fix_journal)
  OR EXISTS(SELECT 1 FROM job_metric_totals WHERE kind='consistency_check' AND (succeeded_total<>0 OR failed_total<>0 OR cancelled_total<>0 OR wait_count<>0 OR wait_sum_microseconds<>0 OR duration_count<>0 OR duration_sum_microseconds<>0))
  OR EXISTS(SELECT 1 FROM job_metric_buckets WHERE kind='consistency_check' AND bucket_count<>0) THEN
  RAISE EXCEPTION 'retained consistency check state prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP INDEX webhook_deliveries_dead_idx;
DROP TABLE consistency_fix_journal;
DROP TABLE consistency_run_checks;
DROP TABLE consistency_runs;
DELETE FROM job_metric_buckets WHERE kind='consistency_check';
DELETE FROM job_metric_totals WHERE kind='consistency_check';
ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync'));
ALTER TABLE jobs DROP CONSTRAINT jobs_consistency_check_error_check;
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed'));
COMMIT;
