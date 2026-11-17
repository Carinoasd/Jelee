BEGIN;
LOCK TABLE scan_schedules,jobs,metadata_refresh_job_state IN ACCESS EXCLUSIVE MODE;
-- G13.1 generic schedules. One definition per kind and scope (a library, or
-- the server for instance-wide kinds) bounds storage by the library count.
-- Only kinds that can run unattended are accepted; destructive or prepared
-- kinds (external_metadata_removal, nfo_*) are refused by the CHECK as well
-- as by the application.
CREATE TABLE job_schedules (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 kind text NOT NULL CHECK (kind IN ('inventory_scan','metadata_refresh','image_refresh','consistency_check','subtitle_cache_cleanup','backup_reminder')),
 library_id uuid REFERENCES libraries(id) ON DELETE CASCADE,
 revision bigint NOT NULL CHECK (revision > 0),
 enabled boolean NOT NULL DEFAULT false,
 mode text NOT NULL CHECK (mode IN ('interval','cron','once')),
 interval_seconds integer NOT NULL,
 cron text NOT NULL CHECK (octet_length(cron) <= 256),
 run_at timestamptz CHECK (run_at IS NULL OR isfinite(run_at)),
 timezone text NOT NULL CHECK (octet_length(timezone) BETWEEN 1 AND 128 AND timezone <> 'Local'),
 window_start text NOT NULL DEFAULT '' CHECK (window_start = '' OR window_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
 window_end text NOT NULL DEFAULT '' CHECK (window_end = '' OR window_end ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
 params jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(params) = 'object' AND octet_length(params::text) <= 1024),
 next_due timestamptz,
 retry_after timestamptz,
 last_job_id uuid REFERENCES jobs(id) ON DELETE SET NULL,
 last_error text NOT NULL DEFAULT '' CHECK (last_error IN ('','admission_unavailable','kind_unavailable','owner_unavailable')),
 last_run_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT job_schedules_scope_check CHECK ((kind IN ('subtitle_cache_cleanup','backup_reminder')) = (library_id IS NULL)),
 CONSTRAINT job_schedules_window_check CHECK ((window_start = '') = (window_end = '') AND (window_start = '' OR window_start <> window_end)),
 CONSTRAINT job_schedules_timing_check CHECK ((mode = 'interval' AND interval_seconds BETWEEN 60 AND 31536000 AND cron = '' AND run_at IS NULL)
  OR (mode = 'cron' AND interval_seconds = 0 AND octet_length(cron) > 0 AND run_at IS NULL)
  OR (mode = 'once' AND interval_seconds = 0 AND cron = '' AND run_at IS NOT NULL)),
 CONSTRAINT job_schedules_due_check CHECK (enabled = (next_due IS NOT NULL))
);
CREATE UNIQUE INDEX job_schedules_scope ON job_schedules(kind,COALESCE(library_id,'00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX job_schedules_due ON job_schedules(next_due,id) WHERE enabled;
CREATE INDEX job_schedules_library ON job_schedules(library_id) WHERE library_id IS NOT NULL;
CREATE INDEX job_schedules_last_job ON job_schedules(last_job_id) WHERE last_job_id IS NOT NULL;

-- Library scan schedules move here unchanged: same timing, zone, due time,
-- retry and last job. scan_schedules keeps the scan options, the directory
-- watch and its owner, which the watch still runs as.
INSERT INTO job_schedules(kind,library_id,revision,enabled,mode,interval_seconds,cron,timezone,params,next_due,retry_after,last_job_id,last_error,updated_at)
SELECT 'inventory_scan',library_id,revision,enabled,mode,interval_seconds,cron,timezone,
 jsonb_build_object('probe',probe,'nfo',nfo,'ignore',jsonb_build_object('mode',ignore_mode,'caseMode',ignore_case)),
 next_due,retry_after,last_job_id,last_error,updated_at
FROM scan_schedules;
ALTER TABLE scan_schedules DROP COLUMN enabled,DROP COLUMN mode,DROP COLUMN interval_seconds,DROP COLUMN cron,DROP COLUMN timezone,
 DROP COLUMN next_due,DROP COLUMN retry_after,DROP COLUMN last_job_id,DROP COLUMN last_error;

-- A job queued by a schedule or automatically after a synchronisation runs
-- as the system actor: no submitting user, and its source recorded here. Only
-- the schedulable kinds may carry it.
ALTER TABLE jobs ADD COLUMN system_source text,
 ADD CONSTRAINT jobs_system_source_check CHECK (system_source IS NULL OR (actor_id IS NULL
  AND kind IN ('inventory_scan','metadata_refresh','image_refresh','consistency_check','subtitle_cache_cleanup','backup_reminder')
  AND system_source ~ '^system:(schedule|catalog_sync):[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'));
ALTER TABLE metadata_refresh_job_state DROP CONSTRAINT metadata_refresh_job_state_trigger_check;
ALTER TABLE metadata_refresh_job_state ADD CONSTRAINT metadata_refresh_job_state_trigger_check CHECK (trigger IN ('manual','catalog_sync','schedule'));
COMMIT;
