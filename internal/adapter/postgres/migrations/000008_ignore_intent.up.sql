BEGIN;

-- Retain the opt-in on the parent independently of its request row. Existing
-- jobs remain off; no filesystem observation or new intent is inferred here.
ALTER TABLE jobs ADD COLUMN ignore_requested boolean NOT NULL DEFAULT false;
CREATE FUNCTION job_ignore_requested_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.ignore_requested IS DISTINCT FROM OLD.ignore_requested THEN
  RAISE EXCEPTION 'job ignore intent is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_ignore_requested_immutable BEFORE UPDATE OF ignore_requested ON jobs
 FOR EACH ROW EXECUTE FUNCTION job_ignore_requested_immutable();

CREATE TABLE job_ignore_requests (
 job_id uuid PRIMARY KEY,
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 mode text NOT NULL DEFAULT 'jeleeignore' CHECK(mode='jeleeignore'),
 case_mode text NOT NULL CHECK(case_mode IN ('sensitive','ascii-insensitive')),
 program_version text NOT NULL CHECK(program_version='jeleeignore-v1'),
 proof_version text NOT NULL CHECK(proof_version='jeleeignore-proof-v1'),
 FOREIGN KEY(job_id,library_id) REFERENCES jobs(id,library_id) ON DELETE CASCADE
);
CREATE INDEX job_ignore_requests_library_idx ON job_ignore_requests(library_id);
CREATE FUNCTION job_ignore_request_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'job ignore request is immutable' USING ERRCODE='23514';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM jobs WHERE id=NEW.job_id AND library_id=NEW.library_id AND ignore_requested) THEN
  RAISE EXCEPTION 'job ignore request requires retained intent' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_ignore_request_immutable BEFORE INSERT OR UPDATE ON job_ignore_requests
 FOR EACH ROW EXECUTE FUNCTION job_ignore_request_immutable();

COMMIT;
