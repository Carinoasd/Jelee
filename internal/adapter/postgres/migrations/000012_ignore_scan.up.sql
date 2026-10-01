BEGIN;
CREATE TABLE job_ignore_scan_state (
 job_id uuid PRIMARY KEY REFERENCES job_ignore_requests(job_id) ON DELETE CASCADE,
 excluded_files bigint NOT NULL DEFAULT 0 CHECK(excluded_files BETWEEN 0 AND 500000),
 excluded_directories bigint NOT NULL DEFAULT 0 CHECK(excluded_directories BETWEEN 0 AND 100000)
);
CREATE TABLE job_ignore_exclusions (
 job_id uuid NOT NULL REFERENCES job_ignore_scan_state(job_id) ON DELETE CASCADE,
 root_id uuid NOT NULL REFERENCES library_roots(id),
 parent_path text NOT NULL CHECK(octet_length(parent_path) BETWEEN 1 AND 1024),
 path text COLLATE "C" NOT NULL CHECK(octet_length(path) BETWEEN 1 AND 1024),
 kind text NOT NULL CHECK(kind IN ('directory','video','nfo','image','other')),
 rule_directory text NOT NULL CHECK(octet_length(rule_directory) BETWEEN 1 AND 1024),
 rule_line integer NOT NULL CHECK(rule_line BETWEEN 1 AND 4096),
 matched_path text NOT NULL CHECK(octet_length(matched_path) BETWEEN 1 AND 1024),
 PRIMARY KEY(job_id,root_id,path),
 FOREIGN KEY(job_id,root_id,parent_path) REFERENCES job_directories(job_id,root_id,path) ON DELETE CASCADE
);
CREATE INDEX job_ignore_exclusions_parent_idx ON job_ignore_exclusions(job_id,root_id,parent_path);
CREATE TRIGGER job_ignore_exclusions_frozen BEFORE INSERT OR UPDATE OR DELETE ON job_ignore_exclusions FOR EACH ROW EXECUTE FUNCTION job_ignore_inventory_frozen();
COMMIT;
