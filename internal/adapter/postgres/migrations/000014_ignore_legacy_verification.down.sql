BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_ignore_legacy_verifications) THEN
  RAISE EXCEPTION 'retained legacy verification prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE job_ignore_legacy_verifications;
COMMIT;
