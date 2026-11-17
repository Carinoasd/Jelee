BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets,image_refresh_job_state IN ACCESS EXCLUSIVE MODE;
-- Refresh jobs and their progress are retained state: an older binary could
-- neither resume nor display them. Fetch failures only defer retries and
-- are dropped; fetched content stays in item_images.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM jobs WHERE kind='image_refresh' OR error_code='image_refresh_failed')
  OR EXISTS(SELECT 1 FROM image_refresh_job_state)
  OR EXISTS(SELECT 1 FROM job_metric_totals WHERE kind='image_refresh' AND (succeeded_total<>0 OR failed_total<>0 OR cancelled_total<>0 OR wait_count<>0 OR wait_sum_microseconds<>0 OR duration_count<>0 OR duration_sum_microseconds<>0))
  OR EXISTS(SELECT 1 FROM job_metric_buckets WHERE kind='image_refresh' AND bucket_count<>0) THEN
  RAISE EXCEPTION 'retained image refresh state prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP INDEX item_images_refresh_idx;
DROP TABLE item_image_fetch_failures;
DROP TABLE image_refresh_job_state;
DELETE FROM job_metric_buckets WHERE kind='image_refresh';
DELETE FROM job_metric_totals WHERE kind='image_refresh';
ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export'));
ALTER TABLE jobs DROP CONSTRAINT jobs_image_refresh_error_check;
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed','consistency_check_failed','nfo_apply_failed','nfo_export_failed'));
COMMIT;
