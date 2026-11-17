BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets,subtitle_cache_cleanup_job_state,backup_reminder_job_state,external_metadata_removal_job_state IN ACCESS EXCLUSIVE MODE;
-- Jobs of the three kinds, their progress and their metric samples are
-- retained state: an older binary could neither run nor display them.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM jobs WHERE kind IN ('subtitle_cache_cleanup','backup_reminder','external_metadata_removal') OR error_code IN ('subtitle_cache_cleanup_failed','backup_reminder_failed','external_metadata_removal_failed'))
  OR EXISTS(SELECT 1 FROM subtitle_cache_cleanup_job_state)
  OR EXISTS(SELECT 1 FROM backup_reminder_job_state)
  OR EXISTS(SELECT 1 FROM external_metadata_removal_job_state)
  OR EXISTS(SELECT 1 FROM job_metric_totals WHERE kind IN ('subtitle_cache_cleanup','backup_reminder','external_metadata_removal') AND (succeeded_total<>0 OR failed_total<>0 OR cancelled_total<>0 OR wait_count<>0 OR wait_sum_microseconds<>0 OR duration_count<>0 OR duration_sum_microseconds<>0))
  OR EXISTS(SELECT 1 FROM job_metric_buckets WHERE kind IN ('subtitle_cache_cleanup','backup_reminder','external_metadata_removal') AND bucket_count<>0) THEN
  RAISE EXCEPTION 'retained maintenance job state prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE external_metadata_removal_job_state;
DROP TABLE backup_reminder_job_state;
DROP TABLE subtitle_cache_cleanup_job_state;
DELETE FROM job_metric_buckets WHERE kind IN ('subtitle_cache_cleanup','backup_reminder','external_metadata_removal');
DELETE FROM job_metric_totals WHERE kind IN ('subtitle_cache_cleanup','backup_reminder','external_metadata_removal');
ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export','image_refresh','metadata_refresh'));
ALTER TABLE jobs DROP CONSTRAINT jobs_external_metadata_removal_error_check;
ALTER TABLE jobs DROP CONSTRAINT jobs_backup_reminder_error_check;
ALTER TABLE jobs DROP CONSTRAINT jobs_subtitle_cache_cleanup_error_check;
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export','image_refresh','metadata_refresh'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed','consistency_check_failed','nfo_apply_failed','nfo_export_failed','image_refresh_failed','metadata_refresh_failed'));
COMMIT;
