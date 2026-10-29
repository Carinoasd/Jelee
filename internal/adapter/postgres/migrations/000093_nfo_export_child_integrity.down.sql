BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
-- Nothing is retained that an older schema could not hold: it only loses the
-- rule that binds a child to its export job.
DROP TRIGGER guard_job_export_children ON jobs;
DROP FUNCTION guard_job_export_children();
DROP TRIGGER guard_job_export_parent ON jobs;
DROP FUNCTION guard_job_export_parent();
DROP INDEX jobs_export_job_idx;
ALTER TABLE jobs DROP CONSTRAINT jobs_export_job_fkey;
COMMIT;
