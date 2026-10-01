BEGIN;
-- Serialize admission with the guard and DDL. Terminal retained jobs still
-- carry retry intent, so ordinary history cleanup must remove them first.
LOCK TABLE jobs,job_ignore_requests IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM jobs WHERE ignore_requested) OR EXISTS(SELECT 1 FROM job_ignore_requests) THEN
  RAISE EXCEPTION 'retained ignore intent prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE job_ignore_requests;
DROP FUNCTION job_ignore_request_immutable();
DROP TRIGGER job_ignore_requested_immutable ON jobs;
DROP FUNCTION job_ignore_requested_immutable();
ALTER TABLE jobs DROP COLUMN ignore_requested;
COMMIT;
