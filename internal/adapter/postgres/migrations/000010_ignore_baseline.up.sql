BEGIN;
ALTER TABLE libraries ADD COLUMN inventory_baseline_revision bigint NOT NULL DEFAULT 1 CHECK(inventory_baseline_revision>0);
ALTER TABLE library_inventory_baseline ADD COLUMN observed_revision bigint NOT NULL DEFAULT 1 CHECK(observed_revision>0);
CREATE INDEX library_inventory_baseline_cursor_idx ON library_inventory_baseline(library_id,root_id,path COLLATE "C");
CREATE TABLE job_ignore_comparisons (
 job_id uuid PRIMARY KEY REFERENCES job_ignore_manifests(job_id) ON DELETE CASCADE,
 baseline_revision bigint NOT NULL CHECK(baseline_revision>0),
 inventory_generation bigint NOT NULL CHECK(inventory_generation>0),
 baseline_count bigint NOT NULL CHECK(baseline_count BETWEEN 0 AND 500000),
 scope_comparable boolean NOT NULL,
 sequence bigint NOT NULL DEFAULT 0 CHECK(sequence BETWEEN 0 AND 3908),
 after_root_id uuid,
 after_path text COLLATE "C" NOT NULL DEFAULT '',
 processed bigint NOT NULL DEFAULT 0 CHECK(processed BETWEEN 0 AND 500000),
 observed bigint NOT NULL DEFAULT 0 CHECK(observed BETWEEN 0 AND 500000),
 missing bigint NOT NULL DEFAULT 0 CHECK(missing BETWEEN 0 AND 500000),
 excluded bigint NOT NULL DEFAULT 0 CHECK(excluded BETWEEN 0 AND 500000),
 unknown bigint NOT NULL DEFAULT 0 CHECK(unknown BETWEEN 0 AND 500000),
 completed boolean NOT NULL DEFAULT false,
 CHECK((after_root_id IS NULL)=(after_path='')),
 CHECK(processed=observed+missing+excluded+unknown AND processed<=baseline_count),
 CHECK(NOT completed OR NOT scope_comparable OR processed=baseline_count)
);
CREATE TABLE job_ignore_decisions (
 job_id uuid NOT NULL REFERENCES job_ignore_comparisons(job_id) ON DELETE CASCADE,
 root_id uuid NOT NULL REFERENCES library_roots(id),
 path text COLLATE "C" NOT NULL CHECK(octet_length(path) BETWEEN 1 AND 1024),
 outcome text NOT NULL CHECK(outcome IN ('included_missing','excluded','unknown')),
 rule_directory text NOT NULL,
 rule_line integer NOT NULL,
 matched_path text NOT NULL,
 reason text NOT NULL,
 PRIMARY KEY(job_id,root_id,path),
 CHECK((outcome='excluded' AND octet_length(rule_directory) BETWEEN 1 AND 1024 AND rule_line BETWEEN 1 AND 4096 AND octet_length(matched_path) BETWEEN 1 AND 1024 AND reason='') OR
       (outcome='included_missing' AND rule_directory='' AND rule_line=0 AND matched_path='' AND reason='') OR
       (outcome='unknown' AND rule_directory='' AND rule_line=0 AND matched_path='' AND reason IN ('source_unavailable','source_changed','coverage_unknown')))
);
CREATE TABLE job_ignore_comparison_pages (
 job_id uuid NOT NULL REFERENCES job_ignore_comparisons(job_id) ON DELETE CASCADE,
 sequence bigint NOT NULL CHECK(sequence BETWEEN 0 AND 3907),
 digest bytea NOT NULL CHECK(octet_length(digest)=32),
 PRIMARY KEY(job_id,sequence)
);
CREATE FUNCTION job_ignore_inventory_frozen() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target uuid;
BEGIN
 IF TG_OP='DELETE' THEN target=OLD.job_id; ELSE target=NEW.job_id; END IF;
 -- A cascading history deletion has already removed the parent job.
 IF EXISTS(SELECT 1 FROM jobs j JOIN job_ignore_comparisons c ON c.job_id=j.id WHERE j.id=target) THEN
  RAISE EXCEPTION 'ignore comparison freezes inventory' USING ERRCODE='55000';
 END IF;
 IF TG_OP='UPDATE' AND NEW.job_id IS DISTINCT FROM OLD.job_id AND EXISTS(SELECT 1 FROM jobs j JOIN job_ignore_comparisons c ON c.job_id=j.id WHERE j.id=OLD.job_id) THEN
  RAISE EXCEPTION 'ignore comparison freezes inventory' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_ignore_inventory_frozen BEFORE INSERT OR UPDATE OR DELETE ON job_inventory FOR EACH ROW EXECUTE FUNCTION job_ignore_inventory_frozen();
CREATE TRIGGER job_ignore_directories_frozen BEFORE INSERT OR UPDATE OR DELETE ON job_directories FOR EACH ROW EXECUTE FUNCTION job_ignore_inventory_frozen();
COMMIT;
