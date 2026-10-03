BEGIN;
LOCK TABLE jobs,nfo_write_commit_journal,nfo_write_commit_attempt_reservations,nfo_write_commit_attempts,nfo_write_commit_attempt_checkpoints,nfo_write_commit_attempt_ready IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_commit_attempt_reservations) OR EXISTS(SELECT 1 FROM nfo_write_commit_journal) THEN
  RAISE EXCEPTION 'nfo commit attempts or journal are retained' USING ERRCODE='23514';
 END IF;
END $$;
CREATE OR REPLACE FUNCTION guard_nfo_catalog_mutation() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE old_id uuid; new_id uuid; column_name text; entry record;
BEGIN
 IF TG_OP<>'INSERT' THEN old_id:=OLD.id; END IF;
 IF TG_OP<>'DELETE' THEN new_id:=NEW.id; END IF;
 IF TG_TABLE_NAME='items' THEN column_name:='item_id';
 ELSIF TG_TABLE_NAME='libraries' THEN column_name:='library_id';
 ELSE column_name:='root_id'; END IF;
 FOR entry IN EXECUTE format('SELECT DISTINCT e.job_id,e.sequence FROM %I.nfo_write_entries e JOIN %I.nfo_write_commit_journal w ON w.job_id=e.job_id AND w.sequence=e.sequence LEFT JOIN %I.nfo_write_commit_file_plans p ON p.token=w.token LEFT JOIN %I.nfo_write_commit_files_ready r ON r.token=w.token WHERE (e.%I=$1 OR e.%I=$2) AND (%I.nfo_commit_xmin_is_current(w.xmin) OR %I.nfo_commit_xmin_is_current(p.xmin) OR %I.nfo_commit_xmin_is_current(r.xmin) OR EXISTS(SELECT 1 FROM %I.nfo_write_commit_file_checkpoints c WHERE c.token=w.token AND %I.nfo_commit_xmin_is_current(c.xmin)))',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,column_name,column_name,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 USING old_id,new_id LOOP
  EXECUTE format('SELECT %I.check_nfo_commit_catalog($1,$2,$3)',TG_TABLE_SCHEMA) USING TG_TABLE_SCHEMA,entry.job_id,entry.sequence;
 END LOOP;
 RETURN NULL;
END $$;
DROP TRIGGER aa_guard_nfo_legacy_attempt_ready ON nfo_write_commit_files_ready;
DROP TRIGGER aa_guard_nfo_legacy_attempt_checkpoint ON nfo_write_commit_file_checkpoints;
DROP FUNCTION guard_nfo_legacy_attempt_evidence();
DROP TRIGGER reserve_nfo_commit_plan_attempts ON nfo_write_commit_file_plans;
DROP TRIGGER aa_fence_nfo_commit_plan_attempt_quota ON nfo_write_commit_file_plans;
DROP FUNCTION reserve_nfo_commit_plan_attempts();
DROP TABLE nfo_write_commit_attempt_ready,nfo_write_commit_attempt_checkpoints;
ALTER TABLE nfo_write_commit_attempt_reservations DROP CONSTRAINT nfo_attempt_initial_slot;
DROP TABLE nfo_write_commit_attempts,nfo_write_commit_attempt_reservations,nfo_commit_attempt_quota_fence;
DROP FUNCTION verify_nfo_commit_attempt();
DROP FUNCTION seed_nfo_commit_legacy_attempt();
DROP FUNCTION guard_nfo_commit_attempt();
DROP FUNCTION fence_nfo_commit_attempt_quota();
DROP FUNCTION retain_nfo_commit_attempt_quota_fence();
COMMIT;
