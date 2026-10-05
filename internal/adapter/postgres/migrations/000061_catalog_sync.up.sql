BEGIN;
-- No state transition may commit while the job kind and metric dimensions
-- change; the metric trigger requires a totals row for every claimable kind.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE job_metric_totals,job_metric_buckets IN ACCESS EXCLUSIVE MODE;

ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed','catalog_sync_failed'));
ALTER TABLE jobs ADD CONSTRAINT jobs_catalog_sync_error_check CHECK(kind='catalog_sync' OR error_code<>'catalog_sync_failed');

ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write','catalog_sync'));
INSERT INTO job_metric_totals(kind,priority) VALUES('catalog_sync','background'),('catalog_sync','manual');
INSERT INTO job_metric_buckets(kind,priority,measure,bucket_index,upper_bound_microseconds)
 SELECT 'catalog_sync',priority,measure,bucket_index,upper_bound_microseconds
 FROM job_metric_buckets WHERE kind='inventory_scan';

-- Directory claims let several bounded scan slots of one lease work on
-- different directories. A claim is valid only for the lease generation that
-- made it; recovery under a newer generation restarts unfinished directories.
ALTER TABLE job_directories ADD COLUMN claim_generation bigint CHECK(claim_generation>0);
ALTER TABLE job_directories ADD COLUMN claim_token uuid;
ALTER TABLE job_directories ADD CONSTRAINT job_directories_claim_check CHECK((claim_generation IS NULL)=(claim_token IS NULL));

ALTER TABLE libraries ADD COLUMN catalog_sync_auto boolean NOT NULL DEFAULT false;

-- Synchronisation intent. 'accept' first publishes the reviewed scan's
-- observation as the baseline, then synchronises; 'sync' starts at sources.
CREATE TABLE catalog_sync_requests (
 job_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 mode text NOT NULL CHECK(mode IN ('sync','accept')),
 source_job_id uuid REFERENCES jobs(id) ON DELETE SET NULL,
 expected_snapshot bigint NOT NULL CHECK(expected_snapshot>=0),
 expected_revision bigint NOT NULL CHECK(expected_revision>0),
 inventory_generation bigint NOT NULL CHECK(inventory_generation>0),
 phase text NOT NULL CHECK(phase IN ('publish','sources','missing','pending','done')),
 publish_snapshot bigint UNIQUE CHECK(publish_snapshot>0),
 publish_total bigint NOT NULL DEFAULT 0 CHECK(publish_total BETWEEN 0 AND 500000),
 publish_copied bigint NOT NULL DEFAULT 0 CHECK(publish_copied BETWEEN 0 AND 500000),
 publish_cursor uuid,
 publish_cleaned boolean NOT NULL DEFAULT false,
 target_snapshot bigint CHECK(target_snapshot>=0),
 target_revision bigint CHECK(target_revision>0),
 cursor_root uuid,
 cursor_path text CHECK(octet_length(cursor_path) BETWEEN 1 AND 1024),
 missing_cursor uuid,
 examined bigint NOT NULL DEFAULT 0 CHECK(examined>=0),
 created bigint NOT NULL DEFAULT 0 CHECK(created>=0),
 updated bigint NOT NULL DEFAULT 0 CHECK(updated>=0),
 unchanged bigint NOT NULL DEFAULT 0 CHECK(unchanged>=0),
 protected bigint NOT NULL DEFAULT 0 CHECK(protected>=0),
 pending bigint NOT NULL DEFAULT 0 CHECK(pending>=0),
 marked_missing bigint NOT NULL DEFAULT 0 CHECK(marked_missing>=0),
 removed bigint NOT NULL DEFAULT 0 CHECK(removed>=0),
 FOREIGN KEY(job_id,library_id) REFERENCES jobs(id,library_id) ON DELETE CASCADE,
 CHECK(publish_copied<=publish_total),
 CHECK((cursor_root IS NULL)=(cursor_path IS NULL)),
 CHECK(mode='accept' OR phase<>'publish'),
 CHECK(phase='publish' OR (target_snapshot IS NOT NULL AND target_revision IS NOT NULL))
);
CREATE INDEX catalog_sync_requests_source_idx ON catalog_sync_requests(source_job_id) WHERE source_job_id IS NOT NULL;

-- One administrator decision per reviewed scan. Its sync job publishes it.
CREATE TABLE inventory_missing_acceptances (
 job_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
 sync_job_id uuid UNIQUE REFERENCES jobs(id) ON DELETE SET NULL,
 accepted_missing bigint NOT NULL CHECK(accepted_missing>0),
 baseline_snapshot bigint NOT NULL CHECK(baseline_snapshot>=0),
 baseline_revision bigint NOT NULL CHECK(baseline_revision>0),
 inventory_generation bigint NOT NULL CHECK(inventory_generation>0),
 accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 published_at timestamptz,
 FOREIGN KEY(job_id,library_id) REFERENCES jobs(id,library_id) ON DELETE CASCADE
);

-- Structure created from file names. Items stay ordinary catalog items; these
-- rows only remember the grouping key so a rerun reuses instead of duplicating.
CREATE TABLE catalog_scan_items (
 item_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('Movie','Series','Season','Episode')),
 group_digest bytea NOT NULL CHECK(octet_length(group_digest)=32),
 parser_version text NOT NULL CHECK(octet_length(parser_version) BETWEEN 1 AND 64),
 scan_title text NOT NULL CHECK(octet_length(scan_title) BETWEEN 1 AND 1024),
 year integer CHECK(year BETWEEN 1 AND 9999),
 season integer CHECK(season BETWEEN 0 AND 100000),
 episode integer CHECK(episode BETWEEN 0 AND 100000),
 episode_end integer CHECK(episode_end BETWEEN 0 AND 100000),
 FOREIGN KEY(item_id,library_id,kind) REFERENCES items(id,library_id,kind) ON DELETE CASCADE,
 UNIQUE(library_id,kind,group_digest)
);

-- Media sources registered by synchronisation. Sources from explicit imports
-- are never listed here and are never changed or removed by synchronisation.
CREATE TABLE catalog_scan_sources (
 source_id uuid PRIMARY KEY REFERENCES media_sources(id) ON DELETE CASCADE,
 library_id uuid NOT NULL,
 item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 root_id uuid NOT NULL,
 relative_path text NOT NULL CHECK(octet_length(relative_path) BETWEEN 1 AND 1024),
 size bigint NOT NULL CHECK(size>=0),
 modified_unix_nano bigint NOT NULL,
 parser_version text NOT NULL CHECK(octet_length(parser_version) BETWEEN 1 AND 64),
 missing_since timestamptz,
 FOREIGN KEY(root_id,library_id) REFERENCES library_roots(id,library_id) ON DELETE CASCADE,
 UNIQUE(root_id,relative_path)
);
CREATE INDEX catalog_scan_sources_library_idx ON catalog_scan_sources(library_id,source_id);
CREATE INDEX catalog_scan_sources_item_idx ON catalog_scan_sources(item_id);

-- Files whose parse is not confident enough for automatic structure.
CREATE TABLE catalog_scan_pending (
 id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
 library_id uuid NOT NULL,
 root_id uuid NOT NULL,
 relative_path text NOT NULL CHECK(octet_length(relative_path) BETWEEN 1 AND 1024),
 size bigint NOT NULL CHECK(size>=0),
 modified_unix_nano bigint NOT NULL,
 parser_version text NOT NULL CHECK(octet_length(parser_version) BETWEEN 1 AND 64),
 reason text NOT NULL CHECK(reason IN ('confidence','rejected','unsupported','conflict')),
 kind text NOT NULL CHECK(kind IN ('movie','episode','unknown')),
 confidence text NOT NULL CHECK(confidence IN ('none','low','medium','high')),
 title text NOT NULL CHECK(octet_length(title)<=1024),
 year integer CHECK(year BETWEEN 1 AND 9999),
 season integer CHECK(season BETWEEN 0 AND 100000),
 episode integer CHECK(episode BETWEEN 0 AND 100000),
 episode_end integer CHECK(episode_end BETWEEN 0 AND 100000),
 absolute integer CHECK(absolute BETWEEN 0 AND 100000),
 special text NOT NULL CHECK(special IN ('none','season','sp','ova','oad','extra','theatrical')),
 first_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(root_id,relative_path),
 FOREIGN KEY(root_id,library_id) REFERENCES library_roots(id,library_id) ON DELETE CASCADE
);
CREATE INDEX catalog_scan_pending_library_idx ON catalog_scan_pending(library_id,id);

-- File-name values are the lowest-priority metadata source.
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_source_check;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_fields_source_check CHECK(source IN ('existing','manual','tmdb','nfo','scan'));
COMMIT;
