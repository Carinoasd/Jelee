BEGIN;
LOCK TABLE jobs,catalog_import_requests,catalog_import_entries IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM jobs WHERE kind='catalog_import' OR error_code='catalog_import_failed')
 OR EXISTS(SELECT 1 FROM catalog_import_requests) OR EXISTS(SELECT 1 FROM catalog_import_entries) THEN
  RAISE EXCEPTION 'retained catalog import jobs prevent rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE catalog_import_entries;
DROP TABLE catalog_import_requests;
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind='inventory_scan');
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted'));
COMMIT;
