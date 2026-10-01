BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_ignore_requests WHERE mode='jeleeignore-legacy-v1') OR
    EXISTS(SELECT 1 FROM job_ignore_legacy_manifests) THEN
  RAISE EXCEPTION 'retained legacy ignore state prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE job_ignore_legacy_queries;
DROP TABLE job_ignore_legacy_proofs;
DROP FUNCTION job_ignore_legacy_proof_guard();
DROP TABLE job_ignore_legacy_manifests;
DROP FUNCTION job_ignore_legacy_intent_guard();
ALTER TABLE job_ignore_requests DROP CONSTRAINT job_ignore_requests_contract_check;
ALTER TABLE job_ignore_requests ADD CONSTRAINT job_ignore_requests_mode_check CHECK(mode='jeleeignore');
ALTER TABLE job_ignore_requests ADD CONSTRAINT job_ignore_requests_program_version_check CHECK(program_version='jeleeignore-v1');
ALTER TABLE job_ignore_requests ADD CONSTRAINT job_ignore_requests_proof_version_check CHECK(proof_version='jeleeignore-proof-v1');
COMMIT;