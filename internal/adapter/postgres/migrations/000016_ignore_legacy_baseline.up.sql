BEGIN;
ALTER TABLE job_ignore_legacy_manifests ADD COLUMN baseline_queries bigint NOT NULL DEFAULT 0 CHECK(baseline_queries BETWEEN 0 AND 16384);
CREATE TABLE job_ignore_legacy_baseline_queries (
 job_id uuid NOT NULL,
 root_id uuid NOT NULL,
 lookup_directory text COLLATE "C" NOT NULL CHECK(octet_length(lookup_directory) BETWEEN 1 AND 1024),
 source_directory text COLLATE "C" NOT NULL,
 missing_directory text COLLATE "C",
 missing_parent_identity bytea,
 proof_version text NOT NULL CHECK(proof_version='legacy-baseline-source-v1'),
 PRIMARY KEY(job_id,root_id,lookup_directory),
 FOREIGN KEY(job_id,root_id,source_directory) REFERENCES job_ignore_legacy_queries(job_id,root_id,directory) ON DELETE CASCADE,
 CHECK((missing_directory IS NULL AND missing_parent_identity IS NULL AND lookup_directory=source_directory) OR
       (missing_directory IS NOT NULL AND octet_length(missing_directory) BETWEEN 1 AND 1024 AND missing_parent_identity IS NOT NULL AND octet_length(missing_parent_identity)=32 AND missing_parent_identity<>decode(repeat('00',32),'hex')))
);
CREATE INDEX job_ignore_legacy_baseline_missing_idx ON job_ignore_legacy_baseline_queries(job_id,root_id,missing_directory) WHERE missing_directory IS NOT NULL;
CREATE TRIGGER job_ignore_legacy_baseline_immutable BEFORE UPDATE ON job_ignore_legacy_baseline_queries
 FOR EACH ROW EXECUTE FUNCTION job_ignore_proof_immutable();
COMMIT;
