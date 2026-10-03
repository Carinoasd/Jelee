BEGIN;
LOCK TABLE nfo_write_commit_journal,nfo_write_commit_file_checkpoints,nfo_write_commit_files_ready IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_commit_file_checkpoints) OR EXISTS(SELECT 1 FROM nfo_write_commit_journal) THEN
  RAISE EXCEPTION 'nfo commit checkpoints or journal are retained' USING ERRCODE='23514';
 END IF;
END $$;
DROP TRIGGER guard_nfo_ready_checkpoint ON nfo_write_commit_files_ready;
DROP TRIGGER verify_nfo_ready_checkpoint ON nfo_write_commit_files_ready;
DROP TABLE nfo_write_commit_file_checkpoints;
DROP FUNCTION guard_nfo_checkpoint_consistency();
DROP FUNCTION guard_nfo_ready_checkpoint();
CREATE OR REPLACE FUNCTION guard_nfo_catalog_mutation() RETURNS trigger
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
COMMIT;
