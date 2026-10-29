BEGIN;
LOCK TABLE nfo_export_job_state IN ACCESS EXCLUSIVE MODE;
-- A missing-mode export is retained state: an older binary would run it as an
-- export of every field into existing NFOs and could not show its counter.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_export_job_state WHERE mode<>'all' OR skipped_existing<>0) THEN
  RAISE EXCEPTION 'retained nfo export missing state prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE nfo_export_job_state
 DROP CONSTRAINT nfo_export_job_state_existing_mode_check,
 DROP COLUMN skipped_existing,
 DROP COLUMN mode;
COMMIT;
