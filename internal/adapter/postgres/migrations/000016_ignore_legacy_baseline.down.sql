BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_ignore_legacy_baseline_queries) THEN
  RAISE EXCEPTION 'retained legacy baseline evidence prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE job_ignore_legacy_baseline_queries;
ALTER TABLE job_ignore_legacy_manifests DROP COLUMN baseline_queries;
COMMIT;
