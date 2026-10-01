BEGIN;
CREATE TABLE job_ignore_legacy_verifications (
 job_id uuid PRIMARY KEY REFERENCES job_ignore_legacy_manifests(job_id) ON DELETE CASCADE,
 generation bigint NOT NULL CHECK(generation>0),
 deadline timestamptz NOT NULL,
 sequence bigint NOT NULL DEFAULT 0 CHECK(sequence BETWEEN 0 AND 1025),
 after_root_id uuid,
 after_directory text COLLATE "C" NOT NULL DEFAULT '',
 verified_queries bigint NOT NULL DEFAULT 0 CHECK(verified_queries BETWEEN 0 AND 16384),
 digest bytea NOT NULL CHECK(octet_length(digest)=32),
 completed boolean NOT NULL DEFAULT false,
 CHECK((after_root_id IS NULL)=(after_directory=''))
);
COMMIT;
