BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_job_requests r JOIN jobs j ON j.id=r.job_id WHERE j.state IN ('queued','running')) THEN
  RAISE EXCEPTION 'finish or cancel nfo request jobs before rollback';
 END IF;
END $$;
DROP TABLE image_job_state;
ALTER TABLE library_inventory_baseline DROP CONSTRAINT baseline_attributes_shape,
 DROP COLUMN attributes_known,DROP COLUMN kind,DROP COLUMN size,DROP COLUMN modified_unix_nano,DROP COLUMN inventory_generation;
DROP INDEX nfo_cache_current_idx;
DROP INDEX nfo_cache_observation_idx;
ALTER TABLE nfo_cache DROP COLUMN observation_id;
DROP TABLE nfo_job_requests;
DROP FUNCTION nfo_request_identity_immutable();
DROP TRIGGER job_inventory_generation_immutable ON jobs;
DROP FUNCTION job_inventory_generation_immutable();
DROP TRIGGER inventory_root_changed ON library_roots;
DROP FUNCTION inventory_root_changed();
ALTER TABLE jobs DROP COLUMN inventory_generation;
ALTER TABLE libraries DROP COLUMN inventory_generation;
COMMIT;
