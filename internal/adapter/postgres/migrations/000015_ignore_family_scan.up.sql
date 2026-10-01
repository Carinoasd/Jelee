BEGIN;
-- Multiple queries may select one ancestor. Check this reference after all
-- cascaded query deletions, rather than during a sibling cascade.
ALTER TABLE job_ignore_legacy_queries ALTER CONSTRAINT job_ignore_legacy_queries_job_id_root_id_selected_director_fkey DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE job_ignore_family_exclusions (
 job_id uuid NOT NULL REFERENCES job_ignore_scan_state(job_id) ON DELETE CASCADE,
 root_id uuid NOT NULL REFERENCES library_roots(id),
 parent_path text NOT NULL CHECK(octet_length(parent_path) BETWEEN 1 AND 1024),
 path text COLLATE "C" NOT NULL CHECK(octet_length(path) BETWEEN 1 AND 1024),
 kind text NOT NULL CHECK(kind IN ('directory','video','nfo','image','other')),
 family text NOT NULL CHECK(family IN ('jeleeignore','legacy-ignore-021')),
 reason text NOT NULL,
 rule_directory text NOT NULL CHECK(octet_length(rule_directory) BETWEEN 1 AND 1024),
 rule_line integer NOT NULL,
 matched_path text NOT NULL CHECK(matched_path=path),
 CHECK((reason='rule' AND rule_line BETWEEN 1 AND 4096) OR
       (family='legacy-ignore-021' AND reason IN ('blank-source','invalid-source') AND rule_line=0)),
 PRIMARY KEY(job_id,root_id,path),
 FOREIGN KEY(job_id,root_id,parent_path) REFERENCES job_directories(job_id,root_id,path) ON DELETE CASCADE
);
CREATE INDEX job_ignore_family_exclusions_parent_idx ON job_ignore_family_exclusions(job_id,root_id,parent_path);
CREATE TRIGGER job_ignore_family_exclusions_intent BEFORE INSERT ON job_ignore_family_exclusions
 FOR EACH ROW EXECUTE FUNCTION job_ignore_legacy_intent_guard();
CREATE FUNCTION job_ignore_family_inventory_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target uuid;
BEGIN
 IF TG_OP='DELETE' THEN target=OLD.job_id; ELSE target=NEW.job_id; END IF;
 IF EXISTS(SELECT 1 FROM jobs j JOIN job_ignore_requests r ON r.job_id=j.id
   WHERE j.id=target AND r.mode='jeleeignore-legacy-v1' AND
   (EXISTS(SELECT 1 FROM job_ignore_manifests m WHERE m.job_id=j.id AND (m.frozen OR m.invalidated)) OR
    EXISTS(SELECT 1 FROM job_ignore_legacy_manifests m WHERE m.job_id=j.id AND (m.frozen OR m.invalidated)))) THEN
  RAISE EXCEPTION 'family evidence freezes inventory' USING ERRCODE='55000';
 END IF;
 IF TG_OP='UPDATE' AND NEW.job_id IS DISTINCT FROM OLD.job_id AND EXISTS(SELECT 1 FROM job_ignore_requests WHERE job_id=OLD.job_id AND mode='jeleeignore-legacy-v1') THEN
  RAISE EXCEPTION 'inventory cannot change job identity' USING ERRCODE='23514';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_ignore_family_exclusions_guard BEFORE INSERT OR UPDATE OR DELETE ON job_ignore_family_exclusions
 FOR EACH ROW EXECUTE FUNCTION job_ignore_family_inventory_guard();
CREATE TRIGGER job_ignore_family_inventory_guard BEFORE INSERT OR UPDATE OR DELETE ON job_inventory
 FOR EACH ROW EXECUTE FUNCTION job_ignore_family_inventory_guard();
CREATE TRIGGER job_ignore_family_directories_guard BEFORE INSERT OR UPDATE OR DELETE ON job_directories
 FOR EACH ROW EXECUTE FUNCTION job_ignore_family_inventory_guard();
COMMIT;
