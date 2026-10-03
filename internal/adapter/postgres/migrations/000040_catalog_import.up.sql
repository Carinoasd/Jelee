BEGIN;
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed'));
CREATE TABLE catalog_import_requests (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 source_job_id uuid REFERENCES jobs(id) ON DELETE SET NULL,
 intent_digest bytea NOT NULL CHECK(octet_length(intent_digest)=32),
 inventory_generation bigint NOT NULL CHECK(inventory_generation>0),
 baseline_revision bigint NOT NULL CHECK(baseline_revision>0),
 total integer NOT NULL CHECK(total BETWEEN 1 AND 100)
);
CREATE INDEX catalog_import_source_idx ON catalog_import_requests(source_job_id);
CREATE TABLE catalog_import_entries (
 job_id uuid NOT NULL REFERENCES catalog_import_requests(job_id) ON DELETE CASCADE,
 sequence integer NOT NULL CHECK(sequence BETWEEN 1 AND 100),
 entry_id uuid NOT NULL,
 root_id uuid NOT NULL,
 relative_path text NOT NULL CHECK(octet_length(relative_path) BETWEEN 1 AND 1024),
 size bigint NOT NULL CHECK(size>=0),
 modified_unix_nano bigint NOT NULL,
 title text NOT NULL CHECK(octet_length(title) BETWEEN 1 AND 1024),
 kind text NOT NULL CHECK(kind IN ('HomeVideo','Movie','Episode')),
 parent_id uuid,
 completed boolean NOT NULL DEFAULT false,
 item_id uuid,
 source_id uuid,
 CHECK(parent_id IS NULL OR kind='Episode'),
 CHECK((completed AND item_id IS NOT NULL AND source_id IS NOT NULL) OR (NOT completed AND item_id IS NULL AND source_id IS NULL)),
 PRIMARY KEY(job_id,sequence),
 UNIQUE(job_id,entry_id)
);
CREATE INDEX catalog_import_pending_idx ON catalog_import_entries(job_id,sequence) WHERE NOT completed;
COMMIT;
