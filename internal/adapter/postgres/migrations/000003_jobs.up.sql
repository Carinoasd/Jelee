BEGIN;
CREATE TABLE jobs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
 idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
 parent_id uuid,
 kind text NOT NULL DEFAULT 'inventory_scan' CHECK (kind='inventory_scan'),
 state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','succeeded','failed','cancelled')),
 priority text NOT NULL CHECK (priority IN ('manual','background')),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
 cancel_requested boolean NOT NULL DEFAULT false,
 files bigint NOT NULL DEFAULT 0 CHECK (files>=0),
 directories bigint NOT NULL DEFAULT 0 CHECK (directories>=0),
 directory_total bigint NOT NULL DEFAULT 0 CHECK (directory_total>=0),
 skipped bigint NOT NULL DEFAULT 0 CHECK (skipped>=0),
 bytes bigint NOT NULL DEFAULT 0 CHECK (bytes>=0),
 missing bigint NOT NULL DEFAULT 0 CHECK (missing>=0),
 review_required boolean NOT NULL DEFAULT false,
 error_code text NOT NULL DEFAULT '' CHECK (error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted')),
 owner text,
 generation bigint NOT NULL DEFAULT 0 CHECK (generation>=0),
 lease_until timestamptz,
 queue_limit integer NOT NULL CHECK (queue_limit BETWEEN 1 AND 1000),
 history_limit integer NOT NULL CHECK (history_limit BETWEEN 1 AND 100),
 max_entries integer NOT NULL CHECK (max_entries BETWEEN 100 AND 500000),
 max_directories integer NOT NULL CHECK (max_directories BETWEEN 1 AND 100000),
 max_attempts integer NOT NULL CHECK (max_attempts BETWEEN 1 AND 10),
 missing_count_limit integer NOT NULL CHECK (missing_count_limit BETWEEN 1 AND 500000),
 missing_percent_limit integer NOT NULL CHECK (missing_percent_limit BETWEEN 1 AND 100),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 started_at timestamptz,
 finished_at timestamptz,
 UNIQUE(actor_id,idempotency_key),
 CHECK ((state='running' AND owner IS NOT NULL AND lease_until IS NOT NULL) OR (state<>'running' AND owner IS NULL AND lease_until IS NULL)),
 CHECK ((state IN ('succeeded','failed','cancelled'))=(finished_at IS NOT NULL))
);
CREATE UNIQUE INDEX jobs_active_library_idx ON jobs(library_id) WHERE state IN ('queued','running');
CREATE INDEX jobs_queue_idx ON jobs(priority,created_at,id) WHERE state='queued';
CREATE INDEX jobs_lease_idx ON jobs(lease_until) WHERE state='running';
CREATE INDEX jobs_history_idx ON jobs(finished_at,id) WHERE state IN ('succeeded','failed','cancelled');
CREATE TABLE job_directories (
 job_id uuid NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 root_id uuid NOT NULL REFERENCES library_roots(id),
 path text NOT NULL CHECK (octet_length(path) BETWEEN 1 AND 1024),
 parent_path text,
 done boolean NOT NULL DEFAULT false,
 skipped bigint NOT NULL DEFAULT 0 CHECK (skipped>=0),
 PRIMARY KEY(job_id,root_id,path)
);
CREATE INDEX job_directories_pending_idx ON job_directories(job_id,root_id,path) WHERE NOT done;
CREATE TABLE job_inventory (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 job_id uuid NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 root_id uuid NOT NULL REFERENCES library_roots(id),
 parent_path text NOT NULL,
 path text NOT NULL CHECK (octet_length(path) BETWEEN 1 AND 1024),
 kind text NOT NULL CHECK (kind IN ('video','nfo','image','other')),
 size bigint NOT NULL CHECK (size>=0),
 modified_unix_nano bigint NOT NULL,
 UNIQUE(job_id,root_id,path)
);
CREATE INDEX job_inventory_page_idx ON job_inventory(job_id,id);
CREATE INDEX job_inventory_parent_idx ON job_inventory(job_id,root_id,parent_path);
-- Keep the last complete successful observation independent of history retention.
-- A partial scan never replaces this baseline. It contains no absolute path.
CREATE TABLE library_inventory_baseline (
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 root_id uuid NOT NULL REFERENCES library_roots(id),
 path text NOT NULL CHECK (octet_length(path) BETWEEN 1 AND 1024),
 PRIMARY KEY(library_id,root_id,path)
);
COMMIT;
