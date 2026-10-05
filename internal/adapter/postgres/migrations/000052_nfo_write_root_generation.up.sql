BEGIN;
LOCK TABLE nfo_write_preparations,nfo_write_entries,nfo_write_commit_journal IN ACCESS EXCLUSIVE MODE;
-- NULL retains the fact that historical data lacks this observation. Never
-- backfill it from today's root, which may be a different generation.
ALTER TABLE nfo_write_preparations ADD COLUMN root_generation bigint CHECK(root_generation>0);
ALTER TABLE nfo_write_entries ADD COLUMN root_generation bigint CHECK(root_generation>0);

CREATE FUNCTION guard_nfo_write_root_generation() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE live_generation bigint; live_path text;
BEGIN
 IF TG_OP='UPDATE' AND OLD.root_generation IS NULL AND NEW.root_generation IS NULL THEN
  RETURN NEW;
 END IF;
 EXECUTE format('SELECT nfo_generation,path FROM %I.library_roots WHERE id=$1 AND library_id=$2 FOR UPDATE',TG_TABLE_SCHEMA)
 INTO live_generation,live_path USING NEW.root_id,NEW.library_id;
 IF NEW.root_generation IS NULL OR live_generation IS DISTINCT FROM NEW.root_generation OR live_path IS DISTINCT FROM NEW.root_path THEN
  RAISE EXCEPTION 'nfo write root generation changed or missing' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_nfo_write_preparation_root BEFORE INSERT OR UPDATE ON nfo_write_preparations
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_root_generation();
CREATE TRIGGER guard_nfo_write_entry_root BEFORE INSERT OR UPDATE ON nfo_write_entries
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_root_generation();
CREATE CONSTRAINT TRIGGER verify_nfo_write_preparation_root AFTER INSERT OR UPDATE ON nfo_write_preparations
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_root_generation();
CREATE CONSTRAINT TRIGGER verify_nfo_write_entry_root AFTER INSERT OR UPDATE ON nfo_write_entries
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_root_generation();

-- The existing journal/plan/ready and catalog-mutation triggers call this name.
-- Preserve the published implementation and add the independent root fence.
ALTER FUNCTION check_nfo_commit_catalog(text,uuid,integer) RENAME TO check_nfo_commit_catalog_v51;
CREATE FUNCTION check_nfo_commit_catalog(schema_name text,job uuid,seq integer) RETURNS void
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE retained bigint; live_generation bigint;
BEGIN
 EXECUTE format('SELECT %I.check_nfo_commit_catalog_v51($1,$2,$3)',schema_name) USING schema_name,job,seq;
 EXECUTE format('SELECT e.root_generation,r.nfo_generation FROM %I.nfo_write_entries e JOIN %I.library_roots r ON r.id=e.root_id AND r.library_id=e.library_id WHERE e.job_id=$1 AND e.sequence=$2 FOR UPDATE OF r',schema_name,schema_name)
 INTO retained,live_generation USING job,seq;
 IF retained IS NULL OR retained IS DISTINCT FROM live_generation THEN
  RAISE EXCEPTION 'nfo commit root generation changed or missing' USING ERRCODE='23514';
 END IF;
END $$;

-- Flushed deferred events cannot permit a subsequent mutation in this same
-- transaction. Historical rows in other transactions do not freeze root edits.
CREATE FUNCTION guard_nfo_write_root_mutation() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE stale boolean; old_id uuid; new_id uuid;
BEGIN
 old_id:=OLD.id;
 IF TG_OP='UPDATE' THEN
  new_id:=NEW.id;
  IF NEW.nfo_generation<OLD.nfo_generation THEN
   RAISE EXCEPTION 'nfo root generation cannot regress' USING ERRCODE='23514';
  END IF;
 END IF;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_preparations p LEFT JOIN %I.library_roots r ON r.id=p.root_id AND r.library_id=p.library_id WHERE (p.root_id=$1 OR p.root_id=$2) AND p.root_generation IS NOT NULL AND (r.nfo_generation IS DISTINCT FROM p.root_generation OR r.path IS DISTINCT FROM p.root_path) AND %I.nfo_commit_xmin_is_current(p.xmin)) OR EXISTS(SELECT 1 FROM %I.nfo_write_entries e LEFT JOIN %I.library_roots r ON r.id=e.root_id AND r.library_id=e.library_id WHERE (e.root_id=$1 OR e.root_id=$2) AND e.root_generation IS NOT NULL AND (r.nfo_generation IS DISTINCT FROM e.root_generation OR r.path IS DISTINCT FROM e.root_path) AND %I.nfo_commit_xmin_is_current(e.xmin))',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO stale USING old_id,new_id;
 IF stale THEN RAISE EXCEPTION 'nfo write root changed after observation' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER guard_nfo_write_root_mutation AFTER UPDATE OR DELETE ON library_roots
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_root_mutation();
COMMIT;
