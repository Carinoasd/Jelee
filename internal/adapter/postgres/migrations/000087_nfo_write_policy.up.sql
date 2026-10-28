BEGIN;
LOCK TABLE libraries,jobs,nfo_write_entries IN ACCESS EXCLUSIVE MODE;
-- NFO writes need their own per-library permission on top of NFO reading
-- (G39.6 read-write). Reading code keeps checking nfo_mode alone, so turning
-- writes on or off neither changes nfo_generation nor disturbs read jobs.
ALTER TABLE libraries ADD COLUMN nfo_write_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE libraries ADD CONSTRAINT libraries_nfo_write_needs_read CHECK(NOT nfo_write_enabled OR nfo_mode='read-only');
-- Every evidence, journal and settlement write already runs the catalog scope
-- check; it now also requires the permission. Rollback settlements skip that
-- check, so a token stopped by revoked permission can still be restored.
ALTER FUNCTION check_nfo_commit_catalog(text,uuid,integer) RENAME TO check_nfo_commit_catalog_v86;
CREATE FUNCTION check_nfo_commit_catalog(schema_name text,job uuid,seq integer) RETURNS void
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE allowed boolean;
BEGIN
 EXECUTE format('SELECT %I.check_nfo_commit_catalog_v86($1,$2,$3)',schema_name) USING schema_name,job,seq;
 EXECUTE format('SELECT l.nfo_write_enabled FROM %I.jobs j JOIN %I.libraries l ON l.id=j.library_id WHERE j.id=$1',schema_name,schema_name)
 INTO allowed USING job;
 IF allowed IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo write disabled' USING ERRCODE='23514';
 END IF;
END $$;
COMMIT;
