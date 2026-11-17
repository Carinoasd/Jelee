BEGIN;
-- G13.6, G14.7: three more job kinds.
--
-- subtitle_cache_cleanup walks Jelee's own embedded subtitle and font
-- extraction cache (JELEE_MATROSKA_CACHE_ROOT) in source order and removes
-- the directories of deleted sources, revisions idle longer than the job's
-- limit and crashed staging directories, then trims the cache to its size
-- bound. backup_reminder compares the last metadata export with a threshold
-- and records a reminder; it never backs anything up. Both are cache- or
-- instance-wide; jobs.library_id is required and one library has at most one
-- active job, so they are anchored to an idle library at admission.
--
-- external_metadata_removal removes the unlocked provider-sourced fields of
-- every item of one library, one item per checkpoint, with the rules of the
-- single-item removal.
--
-- No state transition may commit while the job kind and metric dimensions
-- change; the metric trigger requires a totals row for every claimable kind.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets IN ACCESS EXCLUSIVE MODE;

ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export','image_refresh','metadata_refresh','subtitle_cache_cleanup','backup_reminder','external_metadata_removal'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed','consistency_check_failed','nfo_apply_failed','nfo_export_failed','image_refresh_failed','metadata_refresh_failed','subtitle_cache_cleanup_failed','backup_reminder_failed','external_metadata_removal_failed'));
ALTER TABLE jobs ADD CONSTRAINT jobs_subtitle_cache_cleanup_error_check CHECK(kind='subtitle_cache_cleanup' OR error_code<>'subtitle_cache_cleanup_failed');
ALTER TABLE jobs ADD CONSTRAINT jobs_backup_reminder_error_check CHECK(kind='backup_reminder' OR error_code<>'backup_reminder_failed');
ALTER TABLE jobs ADD CONSTRAINT jobs_external_metadata_removal_error_check CHECK(kind='external_metadata_removal' OR error_code<>'external_metadata_removal_failed');

ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export','image_refresh','metadata_refresh','subtitle_cache_cleanup','backup_reminder','external_metadata_removal'));
INSERT INTO job_metric_totals(kind,priority) VALUES('subtitle_cache_cleanup','background'),('subtitle_cache_cleanup','manual'),('backup_reminder','background'),('backup_reminder','manual'),('external_metadata_removal','background'),('external_metadata_removal','manual');
INSERT INTO job_metric_buckets(kind,priority,measure,bucket_index,upper_bound_microseconds)
 SELECT k.kind,b.priority,b.measure,b.bucket_index,b.upper_bound_microseconds
 FROM job_metric_buckets b CROSS JOIN (VALUES('subtitle_cache_cleanup'),('backup_reminder'),('external_metadata_removal')) k(kind) WHERE b.kind='inventory_scan';

-- One row per cleanup job. phase 'sources' walks the source directories in
-- ascending name order after cursor_source; 'capacity' trims the cache to
-- its bound; 'done' has nothing left. The counters are committed with the
-- cursor.
CREATE TABLE subtitle_cache_cleanup_job_state (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 max_idle_days integer NOT NULL CHECK(max_idle_days BETWEEN 1 AND 3650),
 phase text NOT NULL DEFAULT 'sources' CHECK(phase IN ('sources','capacity','done')),
 cursor_source uuid,
 sources integer NOT NULL DEFAULT 0 CHECK(sources>=0),
 orphaned integer NOT NULL DEFAULT 0 CHECK(orphaned>=0),
 revisions integer NOT NULL DEFAULT 0 CHECK(revisions>=0),
 capacity integer NOT NULL DEFAULT 0 CHECK(capacity>=0),
 failed integer NOT NULL DEFAULT 0 CHECK(failed>=0),
 freed_bytes bigint NOT NULL DEFAULT 0 CHECK(freed_bytes>=0),
 updated_at timestamptz NOT NULL
);

-- One row per reminder job; the result columns are set together once.
CREATE TABLE backup_reminder_job_state (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 trigger text NOT NULL CHECK(trigger IN ('manual','schedule')),
 threshold_days integer NOT NULL CHECK(threshold_days BETWEEN 1 AND 365),
 status text CHECK(status IN ('ok','overdue','never')),
 last_export_at timestamptz,
 checked_at timestamptz,
 updated_at timestamptz NOT NULL,
 CONSTRAINT backup_reminder_job_state_result_check CHECK((status IS NULL)=(checked_at IS NULL) AND (status IS NULL OR (status='never')=(last_export_at IS NULL)))
);

-- One row per removal job. cursor_item_id is the last item the job finished
-- with; items are visited in ascending ID order.
CREATE TABLE external_metadata_removal_job_state (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 library_id uuid NOT NULL,
 drop_rejected_reviews boolean NOT NULL,
 cursor_item_id uuid,
 removed integer NOT NULL DEFAULT 0 CHECK(removed>=0),
 fields integer NOT NULL DEFAULT 0 CHECK(fields>=0),
 kept integer NOT NULL DEFAULT 0 CHECK(kept>=0),
 skipped integer NOT NULL DEFAULT 0 CHECK(skipped>=0),
 failed integer NOT NULL DEFAULT 0 CHECK(failed>=0),
 reviews integer NOT NULL DEFAULT 0 CHECK(reviews>=0),
 updated_at timestamptz NOT NULL
);
COMMIT;
