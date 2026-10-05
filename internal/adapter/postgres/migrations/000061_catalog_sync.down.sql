BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets,catalog_sync_requests,inventory_missing_acceptances,catalog_scan_items,catalog_scan_sources,catalog_scan_pending IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM jobs WHERE kind='catalog_sync' OR error_code='catalog_sync_failed')
  OR EXISTS(SELECT 1 FROM catalog_sync_requests)
  OR EXISTS(SELECT 1 FROM inventory_missing_acceptances)
  OR EXISTS(SELECT 1 FROM catalog_scan_items)
  OR EXISTS(SELECT 1 FROM catalog_scan_sources)
  OR EXISTS(SELECT 1 FROM catalog_scan_pending)
  OR EXISTS(SELECT 1 FROM item_metadata_fields WHERE source='scan')
  OR EXISTS(SELECT 1 FROM libraries WHERE catalog_sync_auto)
  OR EXISTS(SELECT 1 FROM job_metric_totals WHERE kind='catalog_sync' AND (succeeded_total<>0 OR failed_total<>0 OR cancelled_total<>0 OR wait_count<>0 OR wait_sum_microseconds<>0 OR duration_count<>0 OR duration_sum_microseconds<>0))
  OR EXISTS(SELECT 1 FROM job_metric_buckets WHERE kind='catalog_sync' AND bucket_count<>0) THEN
  RAISE EXCEPTION 'retained catalog synchronisation state prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_source_check;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_fields_source_check CHECK(source IN ('existing','manual','tmdb','nfo'));
DROP TABLE catalog_scan_pending;
DROP TABLE catalog_scan_sources;
DROP TABLE catalog_scan_items;
DROP TABLE inventory_missing_acceptances;
DROP TABLE catalog_sync_requests;
ALTER TABLE libraries DROP COLUMN catalog_sync_auto;
-- Claims are lease-local progress; an older binary restarts unfinished
-- directories from their beginning exactly as after any recovery.
ALTER TABLE job_directories DROP CONSTRAINT job_directories_claim_check;
ALTER TABLE job_directories DROP COLUMN claim_token;
ALTER TABLE job_directories DROP COLUMN claim_generation;
DELETE FROM job_metric_buckets WHERE kind='catalog_sync';
DELETE FROM job_metric_totals WHERE kind='catalog_sync';
ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write'));
ALTER TABLE jobs DROP CONSTRAINT jobs_catalog_sync_error_check;
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed'));
COMMIT;
