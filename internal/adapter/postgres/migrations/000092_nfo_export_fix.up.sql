BEGIN;
-- G39.11 --fix. The nfo_export job gains a third mode: "fix" repairs the few
-- validation issues that can be repaired without guessing (a missing title
-- that the database stores, a missing identifier) through the same controlled
-- edit, child nfo_write job and .jelee.bak backup as every other export. The
-- mode is job state, not a job kind, so job kinds, error codes and metric
-- dimensions are unchanged.
--
-- In fix mode the entries the child job replaced are counted as fixed (written
-- stays zero) and the items with an issue the fix does not repair as unfixable.
LOCK TABLE nfo_export_job_state IN ACCESS EXCLUSIVE MODE;
ALTER TABLE nfo_export_job_state
 DROP CONSTRAINT nfo_export_job_state_mode_check,
 ADD CONSTRAINT nfo_export_job_state_mode_check CHECK(mode IN ('all','missing','fix')),
 ADD COLUMN fixed integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_fixed_check CHECK(fixed>=0),
 ADD COLUMN unfixable integer NOT NULL DEFAULT 0 CONSTRAINT nfo_export_job_state_unfixable_check CHECK(unfixable>=0),
 ADD CONSTRAINT nfo_export_job_state_fix_mode_check CHECK((mode='fix' AND written=0) OR (mode<>'fix' AND fixed=0 AND unfixable=0));
COMMIT;
