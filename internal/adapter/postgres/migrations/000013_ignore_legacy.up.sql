BEGIN;

-- New tuples are reserved for the combined executor. Existing tuples retain
-- their exact versions; current public admission and claims remain unchanged.
ALTER TABLE job_ignore_requests DROP CONSTRAINT job_ignore_requests_mode_check;
ALTER TABLE job_ignore_requests DROP CONSTRAINT job_ignore_requests_program_version_check;
ALTER TABLE job_ignore_requests DROP CONSTRAINT job_ignore_requests_proof_version_check;
ALTER TABLE job_ignore_requests ADD CONSTRAINT job_ignore_requests_contract_check CHECK (
 (mode='jeleeignore' AND program_version='jeleeignore-v1' AND proof_version='jeleeignore-proof-v1') OR
 (mode='jeleeignore-legacy-v1' AND program_version='jeleeignore-legacy-v1' AND proof_version='jeleeignore-legacy-proof-v1')
);

CREATE TABLE job_ignore_legacy_manifests (
 job_id uuid PRIMARY KEY REFERENCES job_ignore_requests(job_id) ON DELETE CASCADE,
 inventory_generation bigint NOT NULL CHECK(inventory_generation>0),
 frozen boolean NOT NULL DEFAULT false,
 invalidated boolean NOT NULL DEFAULT false,
 rows bigint NOT NULL DEFAULT 0 CHECK(rows BETWEEN 0 AND 16384),
 queries bigint NOT NULL DEFAULT 0 CHECK(queries BETWEEN 0 AND 16384),
 source_bytes bigint NOT NULL DEFAULT 0 CHECK(source_bytes BETWEEN 0 AND 67108864),
 charge_bytes bigint NOT NULL DEFAULT 0 CHECK(charge_bytes BETWEEN 0 AND 67108864)
);
CREATE FUNCTION job_ignore_legacy_intent_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM job_ignore_requests WHERE job_id=NEW.job_id AND mode='jeleeignore-legacy-v1'
   AND program_version='jeleeignore-legacy-v1' AND proof_version='jeleeignore-legacy-proof-v1') THEN
  RAISE EXCEPTION 'legacy manifest requires retained family intent' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_ignore_legacy_intent_guard BEFORE INSERT ON job_ignore_legacy_manifests
 FOR EACH ROW EXECUTE FUNCTION job_ignore_legacy_intent_guard();

CREATE TABLE job_ignore_legacy_proofs (
 job_id uuid NOT NULL REFERENCES job_ignore_legacy_manifests(job_id) ON DELETE CASCADE,
 root_id uuid NOT NULL REFERENCES library_roots(id),
 directory text COLLATE "C" NOT NULL CHECK(octet_length(directory) BETWEEN 1 AND 1024),
 parent_path text COLLATE "C",
 parent_identity bytea NOT NULL CHECK(octet_length(parent_identity)=32),
 identity bytea NOT NULL CHECK(octet_length(identity)=32 AND identity<>decode(repeat('00',32),'hex')),
 checked boolean NOT NULL,
 rule_present boolean NOT NULL,
 rule_identity bytea NOT NULL CHECK(octet_length(rule_identity)=32),
 rule_size bigint NOT NULL CHECK(rule_size BETWEEN 0 AND 262144),
 rule_modified_nano bigint NOT NULL,
 rule_sha256 bytea NOT NULL CHECK(octet_length(rule_sha256)=32),
 PRIMARY KEY(job_id,root_id,directory),
 FOREIGN KEY(job_id,root_id,parent_path) REFERENCES job_ignore_legacy_proofs(job_id,root_id,directory),
 CHECK((directory='.')=(parent_path IS NULL)),
 CHECK((directory='.')=(parent_identity=decode(repeat('00',32),'hex'))),
 CHECK(checked OR NOT rule_present),
 CHECK((rule_present AND rule_identity<>decode(repeat('00',32),'hex') AND rule_sha256<>decode(repeat('00',32),'hex')) OR
 (NOT rule_present AND rule_identity=decode(repeat('00',32),'hex') AND rule_sha256=decode(repeat('00',32),'hex') AND rule_size=0 AND rule_modified_nano=0))
);
CREATE FUNCTION job_ignore_legacy_proof_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW IS NOT DISTINCT FROM OLD THEN RETURN NEW; END IF;
 IF OLD.checked OR NOT NEW.checked OR
   ROW(NEW.job_id,NEW.root_id,NEW.directory,NEW.parent_path,NEW.parent_identity,NEW.identity)
   IS DISTINCT FROM ROW(OLD.job_id,OLD.root_id,OLD.directory,OLD.parent_path,OLD.parent_identity,OLD.identity) THEN
  RAISE EXCEPTION 'checked legacy proof is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_ignore_legacy_proof_guard BEFORE UPDATE ON job_ignore_legacy_proofs
 FOR EACH ROW EXECUTE FUNCTION job_ignore_legacy_proof_guard();

CREATE TABLE job_ignore_legacy_queries (
 job_id uuid NOT NULL,
 root_id uuid NOT NULL,
 directory text COLLATE "C" NOT NULL,
 selected_directory text COLLATE "C",
 proof_version text NOT NULL CHECK(proof_version='legacy-nearest-source-v1'),
 PRIMARY KEY(job_id,root_id,directory),
 FOREIGN KEY(job_id,root_id,directory) REFERENCES job_ignore_legacy_proofs(job_id,root_id,directory) ON DELETE CASCADE,
 FOREIGN KEY(job_id,root_id,selected_directory) REFERENCES job_ignore_legacy_proofs(job_id,root_id,directory)
);
CREATE TRIGGER job_ignore_legacy_query_immutable BEFORE UPDATE ON job_ignore_legacy_queries
 FOR EACH ROW EXECUTE FUNCTION job_ignore_proof_immutable();
COMMIT;