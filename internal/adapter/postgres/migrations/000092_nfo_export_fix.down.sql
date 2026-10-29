BEGIN;
LOCK TABLE nfo_export_job_state IN ACCESS EXCLUSIVE MODE;
-- A fix-mode export is retained state: an older binary would run it as an
-- export of every field and could not show its counters.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_export_job_state WHERE mode='fix' OR fixed<>0 OR unfixable<>0) THEN
  RAISE EXCEPTION 'retained nfo export fix state prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE nfo_export_job_state
 DROP CONSTRAINT nfo_export_job_state_fix_mode_check,
 DROP COLUMN unfixable,
 DROP COLUMN fixed,
 DROP CONSTRAINT nfo_export_job_state_mode_check,
 ADD CONSTRAINT nfo_export_job_state_mode_check CHECK(mode IN ('all','missing'));
COMMIT;
