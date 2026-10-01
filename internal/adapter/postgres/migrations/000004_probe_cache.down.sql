BEGIN;
-- Stop/release workers before rollback. Refuse to discard a live phase lease.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM probe_job_state p JOIN jobs j ON j.id=p.job_id WHERE j.state='running' AND j.lease_until>clock_timestamp()) THEN
  RAISE EXCEPTION 'stop probe workers before rollback';
 END IF;
END $$;
DROP TRIGGER probe_mapping_changed ON media_sources;
DROP FUNCTION probe_mapping_changed();
DROP TRIGGER probe_root_changed ON library_roots;
DROP FUNCTION probe_root_changed();
DROP TABLE probe_cache;
DROP TABLE probe_job_state;
DROP TRIGGER probe_identity_immutable ON tool_versions;
DROP FUNCTION probe_identity_immutable();
DROP TABLE tool_versions;
DROP TABLE probe_library_quota;
DROP TABLE probe_cache_quota;
DROP SEQUENCE probe_lease_fence_seq;
ALTER TABLE jobs DROP CONSTRAINT jobs_id_library_unique;
ALTER TABLE items DROP COLUMN probe_generation;
ALTER TABLE library_roots DROP COLUMN probe_generation;
ALTER TABLE libraries DROP COLUMN probe_generation;
COMMIT;
