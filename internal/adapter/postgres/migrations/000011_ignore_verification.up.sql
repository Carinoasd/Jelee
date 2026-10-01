BEGIN;
CREATE TABLE job_ignore_verifications (
 job_id uuid PRIMARY KEY REFERENCES job_ignore_comparisons(job_id) ON DELETE CASCADE,
 generation bigint NOT NULL CHECK(generation>0),
 deadline timestamptz NOT NULL,
 sequence bigint NOT NULL DEFAULT 0 CHECK(sequence BETWEEN 0 AND 129),
 after_root_id uuid,
 after_directory text COLLATE "C" NOT NULL DEFAULT '',
 verified_rows bigint NOT NULL DEFAULT 0 CHECK(verified_rows BETWEEN 0 AND 16384),
 digest bytea NOT NULL CHECK(octet_length(digest)=32),
 completed boolean NOT NULL DEFAULT false,
 sealed_until timestamptz,
 CHECK((after_root_id IS NULL)=(after_directory='')),
 CHECK(sealed_until IS NULL OR (completed AND sealed_until<=deadline))
);
COMMIT;
