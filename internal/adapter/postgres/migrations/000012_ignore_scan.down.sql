BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_ignore_scan_state) THEN
  RAISE EXCEPTION 'retained ignore scan prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TRIGGER job_ignore_exclusions_frozen ON job_ignore_exclusions;
DROP TABLE job_ignore_exclusions;
DROP TABLE job_ignore_scan_state;
COMMIT;
