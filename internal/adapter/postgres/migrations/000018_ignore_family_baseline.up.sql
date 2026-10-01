BEGIN;
CREATE TABLE job_ignore_family_decisions (
 job_id uuid NOT NULL REFERENCES job_ignore_comparisons(job_id) ON DELETE CASCADE,
 root_id uuid NOT NULL REFERENCES library_roots(id),
 path text COLLATE "C" NOT NULL CHECK(octet_length(path) BETWEEN 1 AND 1024),
 outcome text NOT NULL CHECK(outcome IN ('included_missing','excluded','unknown')),
 family text NOT NULL,
 reason text NOT NULL,
 rule_directory text NOT NULL,
 rule_line integer NOT NULL,
 matched_path text NOT NULL,
 PRIMARY KEY(job_id,root_id,path),
 CHECK((outcome='excluded' AND family IN ('jeleeignore','legacy-ignore-021') AND
        octet_length(rule_directory) BETWEEN 1 AND 1024 AND octet_length(matched_path) BETWEEN 1 AND 1024 AND
        (path=matched_path OR starts_with(path,matched_path||'/')) AND
        (rule_directory='.' OR starts_with(matched_path,rule_directory||'/') OR
         (family='legacy-ignore-021' AND rule_directory=matched_path AND matched_path<>path)) AND
        ((reason='rule' AND rule_line BETWEEN 1 AND 4096) OR
         (family='legacy-ignore-021' AND reason IN ('blank-source','invalid-source') AND rule_line=0))) OR
       (outcome='included_missing' AND family='' AND reason='' AND rule_directory='' AND rule_line=0 AND matched_path='') OR
       (outcome='unknown' AND family='' AND reason IN ('source_unavailable','source_changed','coverage_unknown') AND rule_directory='' AND rule_line=0 AND matched_path=''))
);
CREATE TRIGGER job_ignore_family_decisions_intent BEFORE INSERT ON job_ignore_family_decisions
 FOR EACH ROW EXECUTE FUNCTION job_ignore_legacy_intent_guard();
CREATE TRIGGER job_ignore_family_decisions_immutable BEFORE UPDATE ON job_ignore_family_decisions
 FOR EACH ROW EXECUTE FUNCTION job_ignore_proof_immutable();
CREATE TRIGGER job_ignore_family_decisions_guard BEFORE INSERT OR UPDATE OR DELETE ON job_ignore_family_decisions
 FOR EACH ROW EXECUTE FUNCTION job_ignore_family_inventory_guard();
CREATE TRIGGER job_ignore_family_exclusions_frozen BEFORE INSERT OR UPDATE OR DELETE ON job_ignore_family_exclusions
 FOR EACH ROW EXECUTE FUNCTION job_ignore_inventory_frozen();
COMMIT;
