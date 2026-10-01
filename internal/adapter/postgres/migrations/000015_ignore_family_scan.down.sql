BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_ignore_scan_state s JOIN job_ignore_requests r ON r.job_id=s.job_id WHERE r.mode='jeleeignore-legacy-v1') THEN
  RAISE EXCEPTION 'retained family exclusions prevent rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TRIGGER job_ignore_family_inventory_guard ON job_inventory;
DROP TRIGGER job_ignore_family_directories_guard ON job_directories;
DROP TABLE job_ignore_family_exclusions;
DROP FUNCTION job_ignore_family_inventory_guard();
ALTER TABLE job_ignore_legacy_queries ALTER CONSTRAINT job_ignore_legacy_queries_job_id_root_id_selected_director_fkey NOT DEFERRABLE INITIALLY IMMEDIATE;
COMMIT;
