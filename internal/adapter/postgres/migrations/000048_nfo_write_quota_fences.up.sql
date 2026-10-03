BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE nfo_write_preparations,nfo_write_entries IN SHARE ROW EXCLUSIVE MODE;
-- Advisory locks serialize writers, but cannot refresh a repeatable-read
-- snapshot. Updating a fixed row forces stale snapshots to fail with 40001.
CREATE TABLE nfo_write_quota_fences (
 scope text PRIMARY KEY CHECK(scope IN ('preparation','job_intent'))
);
INSERT INTO nfo_write_quota_fences(scope) VALUES('preparation'),('job_intent');
CREATE FUNCTION fence_nfo_write_quota() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE quota_scope text; lock_key integer; affected bigint;
BEGIN
 IF TG_TABLE_NAME='nfo_write_preparations' THEN
  quota_scope:='preparation'; lock_key:=17481247;
 ELSIF TG_TABLE_NAME='nfo_write_entries' THEN
  quota_scope:='job_intent'; lock_key:=17481248;
 ELSE
  RAISE EXCEPTION 'invalid nfo quota scope' USING ERRCODE='23514';
 END IF;
 PERFORM pg_advisory_xact_lock(hashtext(TG_TABLE_SCHEMA),lock_key);
 EXECUTE format('UPDATE %I.nfo_write_quota_fences SET scope=scope WHERE scope=$1',TG_TABLE_SCHEMA)
 USING quota_scope;
 GET DIAGNOSTICS affected=ROW_COUNT;
 IF affected<>1 THEN
  RAISE EXCEPTION 'nfo quota fence is missing' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
-- BEFORE triggers run in name order: fence before the existing aggregate guard.
CREATE TRIGGER aa_fence_nfo_write_preparation BEFORE INSERT ON nfo_write_preparations
 FOR EACH ROW EXECUTE FUNCTION fence_nfo_write_quota();
CREATE TRIGGER aa_fence_nfo_write_entry BEFORE INSERT ON nfo_write_entries
 FOR EACH ROW EXECUTE FUNCTION fence_nfo_write_quota();
COMMIT;
