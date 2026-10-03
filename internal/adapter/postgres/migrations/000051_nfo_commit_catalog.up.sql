BEGIN;
LOCK TABLE nfo_write_commit_journal,nfo_write_commit_file_plans,nfo_write_commit_files_ready IN SHARE ROW EXCLUSIVE MODE;
-- Keep source/revision phantoms visible to transactions using an old snapshot.
CREATE FUNCTION touch_nfo_catalog_item() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE old_item uuid; new_item uuid;
BEGIN
 IF TG_OP<>'INSERT' THEN old_item:=OLD.item_id; END IF;
 IF TG_OP<>'DELETE' THEN new_item:=NEW.item_id; END IF;
 EXECUTE format('SELECT id FROM %I.items WHERE id=$1 OR id=$2 ORDER BY id FOR UPDATE',TG_TABLE_SCHEMA) USING old_item,new_item;
 EXECUTE format('UPDATE %I.items SET title=title WHERE id=$1 OR id=$2',TG_TABLE_SCHEMA) USING old_item,new_item;
 RETURN NULL;
END $$;
-- Published probe_mapping_changed already touches the item for media mapping
-- insertion/deletion/path changes. Cover the source ID change it omits.
CREATE TRIGGER touch_nfo_catalog_media AFTER UPDATE OF id ON media_sources
 FOR EACH ROW EXECUTE FUNCTION touch_nfo_catalog_item();
CREATE TRIGGER touch_nfo_catalog_directory AFTER INSERT OR UPDATE OR DELETE ON item_directory_sources
 FOR EACH ROW EXECUTE FUNCTION touch_nfo_catalog_item();
CREATE TRIGGER touch_nfo_catalog_revision AFTER INSERT OR UPDATE OR DELETE ON item_metadata_state
 FOR EACH ROW EXECUTE FUNCTION touch_nfo_catalog_item();

CREATE FUNCTION check_nfo_commit_catalog(schema_name text,job uuid,seq integer) RETURNS void
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE intent record; current_item record; policy record; root record; source record; revision bigint; sources bigint; directories bigint;
BEGIN
 EXECUTE format('SELECT e.* FROM %I.nfo_write_entries e JOIN %I.jobs j ON j.id=e.job_id AND j.library_id=e.library_id WHERE e.job_id=$1 AND e.sequence=$2',schema_name,schema_name)
 INTO intent USING job,seq;
 IF intent.item_id IS NULL THEN RAISE EXCEPTION 'nfo catalog intent missing' USING ERRCODE='23514'; END IF;
 EXECUTE format('SELECT library_id,kind FROM %I.items WHERE id=$1 FOR UPDATE',schema_name) INTO current_item USING intent.item_id;
 EXECUTE format('SELECT nfo_mode,nfo_generation FROM %I.libraries WHERE id=$1 FOR UPDATE',schema_name) INTO policy USING intent.library_id;
 EXECUTE format('SELECT path FROM %I.library_roots WHERE id=$1 AND library_id=$2 FOR UPDATE',schema_name) INTO root USING intent.root_id,intent.library_id;
 EXECUTE format('SELECT revision FROM %I.item_metadata_state WHERE item_id=$1 FOR UPDATE',schema_name) INTO revision USING intent.item_id;
 IF current_item.library_id IS DISTINCT FROM intent.library_id OR current_item.kind IS DISTINCT FROM intent.kind
  OR COALESCE(revision,1) IS DISTINCT FROM intent.revision OR policy.nfo_mode IS DISTINCT FROM 'read-only'
  OR policy.nfo_generation IS DISTINCT FROM intent.generation OR root.path IS DISTINCT FROM intent.root_path THEN
  RAISE EXCEPTION 'nfo catalog scope changed' USING ERRCODE='23514';
 END IF;
 IF intent.directory_path<>'' THEN
  EXECUTE format('SELECT id,root_id,relative_path FROM %I.item_directory_sources WHERE item_id=$1 AND library_id=$2 AND kind=$3 FOR UPDATE',schema_name)
  INTO source USING intent.item_id,intent.library_id,intent.kind;
  EXECUTE format('SELECT count(*) FROM %I.media_sources WHERE item_id=$1',schema_name) INTO sources USING intent.item_id;
  IF sources<>0 OR source.id IS DISTINCT FROM intent.source_id OR source.root_id IS DISTINCT FROM intent.root_id
   OR source.relative_path IS DISTINCT FROM intent.directory_path THEN
   RAISE EXCEPTION 'nfo directory scope changed' USING ERRCODE='23514';
  END IF;
 ELSE
  IF intent.kind='Series' THEN
   EXECUTE format('SELECT count(*) FROM %I.item_directory_sources WHERE item_id=$1',schema_name) INTO directories USING intent.item_id;
   IF directories<>0 THEN RAISE EXCEPTION 'nfo series source changed' USING ERRCODE='23514'; END IF;
  END IF;
  EXECUTE format('SELECT count(*) FROM %I.media_sources WHERE item_id=$1',schema_name) INTO sources USING intent.item_id;
  EXECUTE format('SELECT id,root_id,relative_path FROM %I.media_sources WHERE id=$1 AND item_id=$2 AND library_id=$3 FOR UPDATE',schema_name)
  INTO source USING intent.source_id,intent.item_id,intent.library_id;
  IF sources<>1 OR source.id IS DISTINCT FROM intent.source_id OR source.root_id IS DISTINCT FROM intent.root_id
   OR source.relative_path IS DISTINCT FROM intent.media_path THEN
   RAISE EXCEPTION 'nfo media scope changed' USING ERRCODE='23514';
  END IF;
 END IF;
END $$;
CREATE FUNCTION guard_nfo_commit_catalog() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE job uuid; seq integer;
BEGIN
 IF TG_TABLE_NAME='nfo_write_commit_journal' THEN job:=NEW.job_id; seq:=NEW.sequence;
 ELSE
  EXECUTE format('SELECT job_id,sequence FROM %I.nfo_write_commit_journal WHERE token=$1',TG_TABLE_SCHEMA) INTO job,seq USING NEW.token;
 END IF;
 EXECUTE format('SELECT %I.check_nfo_commit_catalog($1,$2,$3)',TG_TABLE_SCHEMA) USING TG_TABLE_SCHEMA,job,seq;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_nfo_commit_catalog_journal BEFORE INSERT OR UPDATE ON nfo_write_commit_journal
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE TRIGGER guard_nfo_commit_catalog_plan BEFORE INSERT OR UPDATE ON nfo_write_commit_file_plans
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE TRIGGER guard_nfo_commit_catalog_ready BEFORE INSERT OR UPDATE ON nfo_write_commit_files_ready
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE CONSTRAINT TRIGGER verify_nfo_commit_catalog_journal AFTER INSERT OR UPDATE ON nfo_write_commit_journal
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE CONSTRAINT TRIGGER verify_nfo_commit_catalog_plan AFTER INSERT OR UPDATE ON nfo_write_commit_file_plans
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE CONSTRAINT TRIGGER verify_nfo_commit_catalog_ready AFTER INSERT OR UPDATE ON nfo_write_commit_files_ready
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
-- Visible uncommitted xmin belongs to this transaction, including subxids.
-- SET CONSTRAINTS can flush deferred events early. A later catalog mutation
-- must still check evidence written/replayed by this transaction. Historical
-- evidence from other transactions remains observable and does not freeze edits.
CREATE FUNCTION nfo_commit_xmin_is_current(v xid) RETURNS boolean
 LANGUAGE plpgsql STRICT SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE top_id numeric; full_id numeric;
BEGIN
 IF v::text::numeric<3 THEN RETURN false; END IF;
 top_id:=pg_current_xact_id()::text::numeric;
 full_id:=top_id-mod(top_id,4294967296)+v::text::numeric;
 IF full_id<top_id-2147483648 THEN full_id:=full_id+4294967296;
 ELSIF full_id>top_id+2147483648 THEN full_id:=full_id-4294967296; END IF;
 IF full_id<top_id THEN RETURN false; END IF;
 RETURN COALESCE(pg_xact_status(full_id::text::xid8)='in progress',false);
EXCEPTION WHEN invalid_parameter_value THEN RETURN false;
END $$;
CREATE FUNCTION guard_nfo_catalog_mutation() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE old_id uuid; new_id uuid; column_name text; entry record;
BEGIN
 IF TG_OP<>'INSERT' THEN old_id:=OLD.id; END IF;
 IF TG_OP<>'DELETE' THEN new_id:=NEW.id; END IF;
 IF TG_TABLE_NAME='items' THEN column_name:='item_id';
 ELSIF TG_TABLE_NAME='libraries' THEN column_name:='library_id';
 ELSE column_name:='root_id'; END IF;
 FOR entry IN EXECUTE format('SELECT DISTINCT e.job_id,e.sequence FROM %I.nfo_write_entries e JOIN %I.nfo_write_commit_journal w ON w.job_id=e.job_id AND w.sequence=e.sequence LEFT JOIN %I.nfo_write_commit_file_plans p ON p.token=w.token LEFT JOIN %I.nfo_write_commit_files_ready r ON r.token=w.token WHERE (e.%I=$1 OR e.%I=$2) AND (%I.nfo_commit_xmin_is_current(w.xmin) OR %I.nfo_commit_xmin_is_current(p.xmin) OR %I.nfo_commit_xmin_is_current(r.xmin))',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,column_name,column_name,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 USING old_id,new_id LOOP
  EXECUTE format('SELECT %I.check_nfo_commit_catalog($1,$2,$3)',TG_TABLE_SCHEMA) USING TG_TABLE_SCHEMA,entry.job_id,entry.sequence;
 END LOOP;
 RETURN NULL;
END $$;
CREATE TRIGGER guard_nfo_catalog_item_mutation AFTER INSERT OR UPDATE OR DELETE ON items
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_catalog_mutation();
CREATE TRIGGER guard_nfo_catalog_library_mutation AFTER INSERT OR UPDATE OR DELETE ON libraries
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_catalog_mutation();
CREATE TRIGGER guard_nfo_catalog_root_mutation AFTER INSERT OR UPDATE OR DELETE ON library_roots
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_catalog_mutation();
COMMIT;
