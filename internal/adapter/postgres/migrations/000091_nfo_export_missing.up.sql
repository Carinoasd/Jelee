BEGIN;
-- G39.14 batch NFO export, third operation (only missing NFOs). The existing
-- nfo_export job gains a mode: "all" is the behavior of schema 89, "missing"
-- creates the NFO of every item that has none through create preparations.
-- The mode is job state, not a job kind, so the job kinds, error codes and
-- metric dimensions are unchanged. skipped_existing counts items that already
-- have an NFO (or got one while the job ran) and were left alone.
LOCK TABLE nfo_export_job_state IN ACCESS EXCLUSIVE MODE;
ALTER TABLE nfo_export_job_state
 ADD COLUMN mode text NOT NULL DEFAULT 'all' CONSTRAINT nfo_export_job_state_mode_check CHECK(mode IN ('all','missing')),
 ADD COLUMN skipped_existing integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_skipped_existing_check CHECK(skipped_existing>=0),
 ADD CONSTRAINT nfo_export_job_state_existing_mode_check CHECK(mode='missing' OR skipped_existing=0);
COMMIT;
