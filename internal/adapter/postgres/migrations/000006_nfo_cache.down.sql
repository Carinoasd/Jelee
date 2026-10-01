BEGIN;
DROP INDEX job_inventory_nfo_page_idx;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_job_state p JOIN jobs j ON j.id=p.job_id WHERE p.mode='read-only' AND j.state IN ('queued','running')) THEN
  RAISE EXCEPTION 'finish or cancel nfo phase jobs before rollback';
 END IF;
END $$;
DROP TRIGGER nfo_root_changed ON library_roots;
DROP FUNCTION nfo_root_changed();
DROP TABLE nfo_cache;
DROP TABLE nfo_job_state;
DROP FUNCTION nfo_phase_identity_immutable();
DROP TABLE nfo_policy_requests;
DROP TABLE nfo_library_quota;
DROP TABLE nfo_cache_quota;
ALTER TABLE library_roots DROP COLUMN nfo_generation;
ALTER TABLE libraries DROP COLUMN nfo_generation,DROP COLUMN nfo_mode;
COMMIT;
