BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_ignore_legacy_baseline_verifications) THEN
  RAISE EXCEPTION 'retained legacy baseline verification prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE job_ignore_legacy_baseline_verifications;
COMMIT;
