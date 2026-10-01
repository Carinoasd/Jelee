BEGIN;
CREATE TABLE probe_requests (
 job_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 scope text NOT NULL CHECK(scope IN ('incremental','library_rebuild','item_rebuild')),
 target_item_id uuid,
 tool_version_id uuid NOT NULL REFERENCES tool_versions(id) ON DELETE RESTRICT,
 library_generation bigint NOT NULL CHECK(library_generation>0),
 target_item_generation bigint,
 error_code text NOT NULL DEFAULT '' CHECK(error_code IN ('','probe_runtime_unavailable','probe_cache_capacity','probe_invalidated','probe_identity_mismatch')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(job_id,library_id) REFERENCES jobs(id,library_id) ON DELETE CASCADE,
 FOREIGN KEY(target_item_id,library_id) REFERENCES items(id,library_id) ON DELETE RESTRICT,
 CHECK((scope='item_rebuild')=(target_item_id IS NOT NULL)),
 CHECK((target_item_id IS NULL)=(target_item_generation IS NULL)),
 CHECK(target_item_generation IS NULL OR target_item_generation>0)
);
CREATE INDEX probe_requests_tool_idx ON probe_requests(tool_version_id);
CREATE INDEX probe_requests_target_idx ON probe_requests(target_item_id);
CREATE INDEX probe_requests_library_idx ON probe_requests(library_id);
CREATE FUNCTION probe_request_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.job_id,NEW.library_id,NEW.scope,NEW.target_item_id,NEW.tool_version_id,NEW.library_generation,NEW.target_item_generation,NEW.created_at)
    IS DISTINCT FROM ROW(OLD.job_id,OLD.library_id,OLD.scope,OLD.target_item_id,OLD.tool_version_id,OLD.library_generation,OLD.target_item_generation,OLD.created_at)
    OR (OLD.error_code<>'' AND NEW.error_code<>OLD.error_code) THEN
  RAISE EXCEPTION 'probe request is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER probe_request_immutable BEFORE UPDATE ON probe_requests FOR EACH ROW EXECUTE FUNCTION probe_request_immutable();
COMMIT;
