BEGIN;
LOCK TABLE scan_schedules,job_schedules,jobs,metadata_refresh_job_state IN ACCESS EXCLUSIVE MODE;
-- Only library scan schedules that schema 106 can express go back into
-- scan_schedules; anything else is retained configuration or system work an
-- older binary could neither run nor attribute.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_schedules j WHERE j.kind<>'inventory_scan' OR j.mode='once' OR j.window_start<>''
   OR NOT EXISTS(SELECT 1 FROM scan_schedules s WHERE s.library_id=j.library_id)
   OR j.params IS DISTINCT FROM (SELECT jsonb_build_object('probe',s.probe,'nfo',s.nfo,'ignore',jsonb_build_object('mode',s.ignore_mode,'caseMode',s.ignore_case)) FROM scan_schedules s WHERE s.library_id=j.library_id)
   OR j.last_error NOT IN ('','admission_unavailable','owner_unavailable'))
  OR EXISTS(SELECT 1 FROM jobs WHERE system_source IS NOT NULL)
  OR EXISTS(SELECT 1 FROM metadata_refresh_job_state WHERE trigger='schedule') THEN
  RAISE EXCEPTION 'retained job schedules or system jobs prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE metadata_refresh_job_state DROP CONSTRAINT metadata_refresh_job_state_trigger_check;
ALTER TABLE metadata_refresh_job_state ADD CONSTRAINT metadata_refresh_job_state_trigger_check CHECK (trigger IN ('manual','catalog_sync'));
ALTER TABLE jobs DROP CONSTRAINT jobs_system_source_check,DROP COLUMN system_source;
ALTER TABLE scan_schedules ADD COLUMN enabled boolean NOT NULL DEFAULT false,ADD COLUMN mode text,ADD COLUMN interval_seconds integer,ADD COLUMN cron text,
 ADD COLUMN timezone text,ADD COLUMN next_due timestamptz,ADD COLUMN retry_after timestamptz,ADD COLUMN last_job_id uuid,ADD COLUMN last_error text NOT NULL DEFAULT '';
-- A watch-only definition without a timed schedule returns disabled.
UPDATE scan_schedules s SET enabled=COALESCE(j.enabled,false),mode=COALESCE(j.mode,'interval'),interval_seconds=COALESCE(j.interval_seconds,86400),cron=COALESCE(j.cron,''),
 timezone=COALESCE(j.timezone,'UTC'),next_due=j.next_due,retry_after=j.retry_after,last_job_id=j.last_job_id,last_error=COALESCE(j.last_error,'')
FROM scan_schedules x LEFT JOIN job_schedules j ON j.kind='inventory_scan' AND j.library_id=x.library_id WHERE x.library_id=s.library_id;
ALTER TABLE scan_schedules ALTER COLUMN mode SET NOT NULL,ALTER COLUMN interval_seconds SET NOT NULL,ALTER COLUMN cron SET NOT NULL,ALTER COLUMN timezone SET NOT NULL,
 ADD CONSTRAINT scan_schedules_mode_check CHECK (mode IN ('interval','cron')),
 ADD CONSTRAINT scan_schedules_cron_check CHECK (octet_length(cron) <= 256),
 ADD CONSTRAINT scan_schedules_timezone_check CHECK (octet_length(timezone) BETWEEN 1 AND 128 AND timezone <> 'Local'),
 ADD CONSTRAINT scan_schedules_last_job_id_fkey FOREIGN KEY (last_job_id) REFERENCES jobs(id) ON DELETE SET NULL,
 ADD CONSTRAINT scan_schedules_last_error_check CHECK (last_error IN ('','owner_unavailable','admission_unavailable')),
 ADD CONSTRAINT scan_schedules_check1 CHECK ((mode='interval' AND interval_seconds BETWEEN 60 AND 31536000 AND cron='') OR (mode='cron' AND interval_seconds=0 AND octet_length(cron)>0)),
 ADD CONSTRAINT scan_schedules_check2 CHECK (enabled = (next_due IS NOT NULL));
CREATE INDEX scan_schedules_due ON scan_schedules(next_due,library_id) WHERE enabled;
CREATE INDEX scan_schedules_last_job ON scan_schedules(last_job_id) WHERE last_job_id IS NOT NULL;
DROP TABLE job_schedules;
COMMIT;
