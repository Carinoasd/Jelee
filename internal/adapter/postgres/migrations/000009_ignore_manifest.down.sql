BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_ignore_manifests) THEN
  RAISE EXCEPTION 'retained ignore manifest prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE job_ignore_proofs;
DROP FUNCTION job_ignore_proof_immutable();
DROP TABLE job_ignore_manifests;
COMMIT;
