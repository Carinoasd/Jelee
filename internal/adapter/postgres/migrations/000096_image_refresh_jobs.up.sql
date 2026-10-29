BEGIN;
-- G40.9/G40.10 image refresh. One image_refresh job fetches the remote
-- artwork of one library that has no stored content yet into the image
-- store and fills the content columns of its item_images row. Locked and
-- manual rows are never selected and never written; image_refresh_job_state
-- is the job's resume point and its counters.
--
-- No state transition may commit while the job kind and metric dimensions
-- change; the metric trigger requires a totals row for every claimable kind.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets IN ACCESS EXCLUSIVE MODE;

ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export','image_refresh'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed','consistency_check_failed','nfo_apply_failed','nfo_export_failed','image_refresh_failed'));
ALTER TABLE jobs ADD CONSTRAINT jobs_image_refresh_error_check CHECK(kind='image_refresh' OR error_code<>'image_refresh_failed');

ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export','image_refresh'));
INSERT INTO job_metric_totals(kind,priority) VALUES('image_refresh','background'),('image_refresh','manual');
INSERT INTO job_metric_buckets(kind,priority,measure,bucket_index,upper_bound_microseconds)
 SELECT 'image_refresh',priority,measure,bucket_index,upper_bound_microseconds
 FROM job_metric_buckets WHERE kind='inventory_scan';

-- One row per job. cursor_image_id is the last item_images row the job
-- finished with; rows are visited in ascending ID order, so a replacement
-- lease continues after it. The counters are committed with the cursor.
CREATE TABLE image_refresh_job_state (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 library_id uuid NOT NULL,
 cursor_image_id uuid,
 fetched integer NOT NULL DEFAULT 0 CONSTRAINT image_refresh_job_state_fetched_check CHECK(fetched>=0),
 failed integer NOT NULL DEFAULT 0 CONSTRAINT image_refresh_job_state_failed_check CHECK(failed>=0),
 deferred integer NOT NULL DEFAULT 0 CONSTRAINT image_refresh_job_state_deferred_check CHECK(deferred>=0),
 skipped integer NOT NULL DEFAULT 0 CONSTRAINT image_refresh_job_state_skipped_check CHECK(skipped>=0),
 updated_at timestamptz NOT NULL
);

-- The last failed fetch of one remote row: a stable reason (never the URL
-- text, response or host), the HTTP status when one was seen, and the time
-- before which a refresh leaves the row alone. remote_url pins the failure
-- to the URL that failed; a row whose URL changed is a candidate at once.
-- A successful fetch deletes the row; it goes with its image row.
CREATE TABLE item_image_fetch_failures (
 image_id uuid PRIMARY KEY REFERENCES item_images(id) ON DELETE CASCADE,
 remote_url text NOT NULL CHECK(octet_length(remote_url) BETWEEN 9 AND 2048),
 failures integer NOT NULL CHECK(failures BETWEEN 1 AND 1000000),
 reason text NOT NULL CHECK(reason IN ('url_rejected','target_denied','redirect_denied','too_large','content_type_rejected','format_rejected','not_found','status_rejected','rate_limited','unavailable','timeout','store_failed','unsupported','busy','local_only')),
 http_status integer CHECK(http_status BETWEEN 100 AND 599),
 attempted_at timestamptz NOT NULL CHECK(isfinite(attempted_at)),
 retry_at timestamptz NOT NULL CHECK(isfinite(retry_at)),
 CONSTRAINT item_image_fetch_failures_times CHECK(retry_at>=attempted_at)
);

-- Refresh candidates of a library in ID order; only rows still waiting for
-- content carry an entry.
CREATE INDEX item_images_refresh_idx ON item_images(library_id,id) WHERE source_kind='remote' AND content_sha256 IS NULL;
COMMIT;
