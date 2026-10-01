BEGIN;
CREATE TABLE job_ignore_manifests (
 job_id uuid PRIMARY KEY REFERENCES job_ignore_requests(job_id) ON DELETE CASCADE,
 inventory_generation bigint NOT NULL CHECK(inventory_generation>0),
 frozen boolean NOT NULL DEFAULT false,
 invalidated boolean NOT NULL DEFAULT false,
 rows bigint NOT NULL DEFAULT 0 CHECK(rows BETWEEN 0 AND 16384),
 source_bytes bigint NOT NULL DEFAULT 0 CHECK(source_bytes BETWEEN 0 AND 67108864),
 charge_bytes bigint NOT NULL DEFAULT 0 CHECK(charge_bytes BETWEEN 0 AND 67108864)
);
CREATE TABLE job_ignore_proofs (
 job_id uuid NOT NULL REFERENCES job_ignore_manifests(job_id) ON DELETE CASCADE,
 root_id uuid NOT NULL REFERENCES library_roots(id),
 directory text COLLATE "C" NOT NULL CHECK(octet_length(directory) BETWEEN 1 AND 1024),
 parent_path text COLLATE "C",
 parent_identity bytea NOT NULL CHECK(octet_length(parent_identity)=32),
 identity bytea NOT NULL CHECK(octet_length(identity)=32),
 missing_directory boolean NOT NULL,
 rule_present boolean NOT NULL,
 rule_identity bytea NOT NULL CHECK(octet_length(rule_identity)=32),
 rule_size bigint NOT NULL CHECK(rule_size BETWEEN 0 AND 262144),
 rule_modified_nano bigint NOT NULL,
 rule_sha256 bytea NOT NULL CHECK(octet_length(rule_sha256)=32),
 PRIMARY KEY(job_id,root_id,directory),
 FOREIGN KEY(job_id,root_id,parent_path) REFERENCES job_ignore_proofs(job_id,root_id,directory),
 CHECK((directory='.')=(parent_path IS NULL)),
 CHECK((directory='.')=(parent_identity=decode(repeat('00',32),'hex'))),
 CHECK((missing_directory AND directory<>'.' AND NOT rule_present AND identity=decode(repeat('00',32),'hex')) OR (NOT missing_directory AND identity<>decode(repeat('00',32),'hex'))),
 CHECK((rule_present AND rule_identity<>decode(repeat('00',32),'hex') AND rule_sha256<>decode(repeat('00',32),'hex')) OR (NOT rule_present AND rule_identity=decode(repeat('00',32),'hex') AND rule_sha256=decode(repeat('00',32),'hex') AND rule_size=0 AND rule_modified_nano=0))
);
CREATE FUNCTION job_ignore_proof_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'ignore source proof is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_ignore_proof_immutable BEFORE UPDATE ON job_ignore_proofs
 FOR EACH ROW EXECUTE FUNCTION job_ignore_proof_immutable();
COMMIT;
