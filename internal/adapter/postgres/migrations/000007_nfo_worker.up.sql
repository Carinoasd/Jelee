BEGIN;
-- A B-phase has no retained public opt-in. Stop/finish its parent before
-- upgrading; never infer or downgrade the missing submission intent.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_job_state p JOIN jobs j ON j.id=p.job_id WHERE p.mode='read-only' AND j.state IN ('queued','running')) THEN
  RAISE EXCEPTION 'finish or cancel legacy nfo phase jobs before upgrade';
 END IF;
 IF EXISTS(SELECT 1 FROM library_inventory_baseline GROUP BY library_id HAVING count(*)>500000) THEN
  RAISE EXCEPTION 'inventory baseline exceeds supported bound';
 END IF;
END $$;

ALTER TABLE libraries ADD COLUMN inventory_generation bigint NOT NULL DEFAULT 1 CHECK(inventory_generation>0);
ALTER TABLE jobs ADD COLUMN inventory_generation bigint CHECK(inventory_generation>0);
CREATE FUNCTION inventory_root_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target uuid;
BEGIN
 IF TG_OP='UPDATE' AND NEW.path IS NOT DISTINCT FROM OLD.path AND NEW.library_id=OLD.library_id THEN RETURN NEW; END IF;
 FOR target IN SELECT id FROM libraries WHERE id IN (CASE WHEN TG_OP<>'INSERT' THEN OLD.library_id END,CASE WHEN TG_OP<>'DELETE' THEN NEW.library_id END) ORDER BY id LOOP
  UPDATE libraries SET inventory_generation=inventory_generation+1 WHERE id=target;
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER inventory_root_changed BEFORE INSERT OR DELETE OR UPDATE OF path,library_id ON library_roots FOR EACH ROW EXECUTE FUNCTION inventory_root_changed();
CREATE FUNCTION job_inventory_generation_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.inventory_generation IS DISTINCT FROM OLD.inventory_generation THEN
  RAISE EXCEPTION 'job inventory generation is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_inventory_generation_immutable BEFORE UPDATE OF inventory_generation ON jobs FOR EACH ROW EXECUTE FUNCTION job_inventory_generation_immutable();

CREATE TABLE nfo_job_requests (
 job_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 requested boolean NOT NULL,
 mode text NOT NULL CHECK(mode IN ('off','read-only')),
 parser_version text NOT NULL CHECK(octet_length(parser_version) BETWEEN 1 AND 64),
 summary_schema_version integer NOT NULL CHECK(summary_schema_version BETWEEN 1 AND 1000),
 fingerprint_version text NOT NULL CHECK(fingerprint_version='sha256-full-v1'),
 max_source_bytes bigint NOT NULL CHECK(max_source_bytes BETWEEN 1 AND 33554432),
 identity_digest bytea NOT NULL CHECK(octet_length(identity_digest)=32),
 library_generation bigint NOT NULL CHECK(library_generation>0),
 error_code text NOT NULL DEFAULT '' CHECK(error_code IN ('','nfo_disabled','nfo_invalidated','nfo_cache_capacity','nfo_identity_mismatch','nfo_unavailable','nfo_cancelled')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(job_id,library_id) REFERENCES jobs(id,library_id) ON DELETE CASCADE,
 CHECK(requested=(mode='read-only')),
 CHECK(requested OR error_code='')
);
CREATE INDEX nfo_job_requests_library_idx ON nfo_job_requests(library_id);
CREATE FUNCTION nfo_request_identity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.job_id,NEW.library_id,NEW.requested,NEW.mode,NEW.parser_version,NEW.summary_schema_version,NEW.fingerprint_version,NEW.max_source_bytes,NEW.identity_digest,NEW.library_generation,NEW.created_at)
    IS DISTINCT FROM ROW(OLD.job_id,OLD.library_id,OLD.requested,OLD.mode,OLD.parser_version,OLD.summary_schema_version,OLD.fingerprint_version,OLD.max_source_bytes,OLD.identity_digest,OLD.library_generation,OLD.created_at) THEN
  RAISE EXCEPTION 'nfo request identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER nfo_request_identity_immutable BEFORE UPDATE ON nfo_job_requests FOR EACH ROW EXECUTE FUNCTION nfo_request_identity_immutable();

ALTER TABLE nfo_cache ADD COLUMN observation_id uuid NOT NULL DEFAULT gen_random_uuid();
CREATE UNIQUE INDEX nfo_cache_observation_idx ON nfo_cache(library_id,observation_id);
CREATE INDEX nfo_cache_current_idx ON nfo_cache(library_id,identity_digest,library_generation,observation_id);

ALTER TABLE library_inventory_baseline
 ADD COLUMN attributes_known boolean NOT NULL DEFAULT false,
 ADD COLUMN kind text CHECK(kind IN ('video','nfo','image','other')),
 ADD COLUMN size bigint CHECK(size>=0),
 ADD COLUMN modified_unix_nano bigint,
 ADD COLUMN inventory_generation bigint CHECK(inventory_generation>0),
 ADD CONSTRAINT baseline_attributes_shape CHECK(
  (attributes_known AND kind IS NOT NULL AND size IS NOT NULL AND modified_unix_nano IS NOT NULL AND inventory_generation IS NOT NULL)
  OR (NOT attributes_known AND kind IS NULL AND size IS NULL AND modified_unix_nano IS NULL AND inventory_generation IS NULL));
CREATE TABLE image_job_state (
 job_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 added bigint NOT NULL DEFAULT 0 CHECK(added BETWEEN 0 AND 500000),
 changed bigint NOT NULL DEFAULT 0 CHECK(changed BETWEEN 0 AND 500000),
 unchanged bigint NOT NULL DEFAULT 0 CHECK(unchanged BETWEEN 0 AND 500000),
 missing bigint NOT NULL DEFAULT 0 CHECK(missing BETWEEN 0 AND 500000),
 uncompared bigint NOT NULL DEFAULT 0 CHECK(uncompared BETWEEN 0 AND 500000),
 comparison_complete boolean NOT NULL DEFAULT false,
 FOREIGN KEY(job_id,library_id) REFERENCES jobs(id,library_id) ON DELETE CASCADE,
 CHECK(added+changed+unchanged+uncompared<=500000),
 CHECK(comparison_complete OR missing=0)
);
COMMIT;
