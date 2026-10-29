BEGIN;
-- G13.6/G14.1/G14.4/G14.6 metadata refresh (automatic scraping). One
-- metadata_refresh job matches the movies, home videos and series of one
-- library against TMDB, external identifiers first and title and year second,
-- applies matches at or above the library's threshold through the confirmed
-- TMDB apply (locks, manual values and trusted NFO keep priority there) and
-- records every other match for administrator review.
--
-- No state transition may commit while the job kind and metric dimensions
-- change; the metric trigger requires a totals row for every claimable kind.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets IN ACCESS EXCLUSIVE MODE;

ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export','image_refresh','metadata_refresh'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed','consistency_check_failed','nfo_apply_failed','nfo_export_failed','image_refresh_failed','metadata_refresh_failed'));
ALTER TABLE jobs ADD CONSTRAINT jobs_metadata_refresh_error_check CHECK(kind='metadata_refresh' OR error_code<>'metadata_refresh_failed');

ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync','consistency_check','nfo_apply','nfo_export','image_refresh','metadata_refresh'));
INSERT INTO job_metric_totals(kind,priority) VALUES('metadata_refresh','background'),('metadata_refresh','manual');
INSERT INTO job_metric_buckets(kind,priority,measure,bucket_index,upper_bound_microseconds)
 SELECT 'metadata_refresh',priority,measure,bucket_index,upper_bound_microseconds
 FROM job_metric_buckets WHERE kind='inventory_scan';

-- Per-library settings; a library without a row uses the defaults: no
-- automatic refresh after synchronisation, only identifier matches applied
-- without review, local identifiers then TMDB. sources is ordered; 'nfo' is
-- the local identifiers and every other entry a remote provider (only TMDB
-- exists, G14.1).
CREATE TABLE metadata_refresh_settings (
 library_id uuid PRIMARY KEY REFERENCES libraries(id) ON DELETE CASCADE,
 auto boolean NOT NULL DEFAULT false,
 auto_apply text NOT NULL DEFAULT 'high' CHECK(auto_apply IN ('high','medium','never')),
 sources text[] NOT NULL DEFAULT '{nfo,tmdb}' CHECK(sources IN ('{tmdb}','{nfo,tmdb}','{tmdb,nfo}')),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- One row per job. cursor_item_id is the last item the job finished with;
-- items are visited in ascending ID order, so a replacement lease continues
-- after it. The counters are committed with the cursor.
CREATE TABLE metadata_refresh_job_state (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 library_id uuid NOT NULL,
 trigger text NOT NULL CHECK(trigger IN ('manual','catalog_sync')),
 cursor_item_id uuid,
 applied integer NOT NULL DEFAULT 0 CHECK(applied>=0),
 unchanged integer NOT NULL DEFAULT 0 CHECK(unchanged>=0),
 review integer NOT NULL DEFAULT 0 CHECK(review>=0),
 unmatched integer NOT NULL DEFAULT 0 CHECK(unmatched>=0),
 skipped integer NOT NULL DEFAULT 0 CHECK(skipped>=0),
 failed integer NOT NULL DEFAULT 0 CHECK(failed>=0),
 updated_at timestamptz NOT NULL
);

-- Matches waiting for an administrator, or refused by one. A rejected row
-- keeps later refreshes away from the item; accepting a candidate applies it
-- and deletes the row. candidates holds at most five provider candidates
-- (providerId, title, originalTitle, date, similarity), never a URL.
CREATE TABLE metadata_refresh_reviews (
 item_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN ('pending','rejected')),
 resource text NOT NULL CHECK(resource IN ('movie','series')),
 confidence text NOT NULL CHECK(confidence IN ('high','medium','low')),
 reason text NOT NULL CHECK(reason IN ('nfo_tmdb_id','nfo_imdb_id','nfo_tvdb_id','existing_provider','title_year_unique','title_year_ambiguous','title_without_year','year_mismatch','low_similarity','external_ambiguous','external_conflict')),
 query_title text NOT NULL CHECK(octet_length(query_title)<=1024),
 query_year integer CHECK(query_year BETWEEN 1000 AND 9999),
 candidates jsonb NOT NULL CHECK(jsonb_typeof(candidates)='array' AND jsonb_array_length(candidates) BETWEEN 1 AND 5 AND octet_length(candidates::text)<=16384),
 job_id uuid REFERENCES jobs(id) ON DELETE SET NULL,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 rejected_at timestamptz,
 rejected_by uuid REFERENCES users(id) ON DELETE SET NULL,
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE,
 CONSTRAINT metadata_refresh_reviews_rejected_check CHECK((state='rejected')=(rejected_at IS NOT NULL) AND (rejected_by IS NULL OR rejected_at IS NOT NULL))
);
CREATE INDEX metadata_refresh_reviews_library_idx ON metadata_refresh_reviews(library_id,item_id);
CREATE INDEX metadata_refresh_reviews_job_idx ON metadata_refresh_reviews(job_id) WHERE job_id IS NOT NULL;
COMMIT;
