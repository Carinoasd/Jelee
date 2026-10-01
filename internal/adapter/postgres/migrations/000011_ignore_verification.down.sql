BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_ignore_verifications) THEN
  RAISE EXCEPTION 'retained ignore verification prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE job_ignore_verifications;
COMMIT;
