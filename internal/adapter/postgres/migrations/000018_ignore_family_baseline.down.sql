BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_ignore_comparisons c JOIN job_ignore_requests r ON r.job_id=c.job_id WHERE r.mode='jeleeignore-legacy-v1') THEN
  RAISE EXCEPTION 'retained family comparison prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TRIGGER job_ignore_family_exclusions_frozen ON job_ignore_family_exclusions;
DROP TABLE job_ignore_family_decisions;
COMMIT;
