BEGIN;
LOCK TABLE jobs,nfo_write_commit_journal,nfo_write_commit_file_plans,nfo_write_commit_files_ready IN ACCESS EXCLUSIVE MODE;
CREATE TABLE nfo_write_commit_file_checkpoints (
 token uuid NOT NULL REFERENCES nfo_write_commit_file_plans(token) ON DELETE RESTRICT,
 phase smallint NOT NULL CHECK(phase IN(1,2)),
 output_identity bytea NOT NULL CHECK(nfo_commit_identity_valid(output_identity,1)),
 rollback_identity bytea,
 first_phase smallint NOT NULL DEFAULT 1 CHECK(first_phase=1),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at),
 PRIMARY KEY(token,phase),
 UNIQUE(token,output_identity,phase),
 CONSTRAINT nfo_commit_checkpoint_first_output FOREIGN KEY(token,output_identity,first_phase)
  REFERENCES nfo_write_commit_file_checkpoints(token,output_identity,phase) ON DELETE RESTRICT,
 CHECK((phase=1 AND rollback_identity IS NULL) OR
  (phase=2 AND rollback_identity IS NOT NULL AND nfo_commit_identity_valid(rollback_identity,1)
   AND get_byte(output_identity,1)=get_byte(rollback_identity,1) AND output_identity<>rollback_identity))
);
-- Retain only already-persisted complete observations. Plan-only history remains
-- unknown; never observe the filesystem or infer an unfinished stage's identity.
INSERT INTO nfo_write_commit_file_checkpoints(token,phase,output_identity,rollback_identity,recorded_at,lease_until)
 SELECT token,1,output_identity,NULL,recorded_at,lease_until FROM nfo_write_commit_files_ready;
INSERT INTO nfo_write_commit_file_checkpoints(token,phase,output_identity,rollback_identity,recorded_at,lease_until)
 SELECT token,2,output_identity,rollback_identity,recorded_at,lease_until FROM nfo_write_commit_files_ready;

CREATE FUNCTION guard_nfo_checkpoint_consistency() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE retained record;
BEGIN
 EXECUTE format('SELECT output_identity,rollback_identity FROM %I.nfo_write_commit_files_ready WHERE token=$1',TG_TABLE_SCHEMA)
 INTO retained USING NEW.token;
 IF retained.output_identity IS NOT NULL AND (retained.output_identity IS DISTINCT FROM NEW.output_identity
  OR (NEW.phase=2 AND retained.rollback_identity IS DISTINCT FROM NEW.rollback_identity)) THEN
  RAISE EXCEPTION 'nfo checkpoint differs from retained ready' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION guard_nfo_ready_checkpoint() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE phases bigint; matched bigint;
BEGIN
 EXECUTE format('SELECT count(*),count(*) FILTER(WHERE output_identity=$2 AND (phase=1 OR rollback_identity=$3)) FROM %I.nfo_write_commit_file_checkpoints WHERE token=$1',TG_TABLE_SCHEMA)
 INTO phases,matched USING NEW.token,NEW.output_identity,NEW.rollback_identity;
 -- Ready-only historical callers keep their first complete observation. Once
 -- partial evidence exists, neither ready nor its replay can bypass that proof.
 IF phases<>0 AND (phases<>2 OR matched<>2) THEN
  RAISE EXCEPTION 'nfo ready checkpoint incomplete or conflicting' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_nfo_commit_checkpoint BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_file_checkpoints
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_files();
CREATE TRIGGER guard_nfo_z_checkpoint_consistency BEFORE INSERT OR UPDATE ON nfo_write_commit_file_checkpoints
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_checkpoint_consistency();
CREATE TRIGGER guard_nfo_checkpoint_catalog BEFORE INSERT OR UPDATE ON nfo_write_commit_file_checkpoints
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE CONSTRAINT TRIGGER verify_nfo_checkpoint_lease AFTER INSERT OR UPDATE ON nfo_write_commit_file_checkpoints
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_commit_files_lease();
CREATE CONSTRAINT TRIGGER verify_nfo_checkpoint_catalog AFTER INSERT OR UPDATE ON nfo_write_commit_file_checkpoints
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE CONSTRAINT TRIGGER verify_nfo_checkpoint_consistency AFTER INSERT OR UPDATE ON nfo_write_commit_file_checkpoints
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_checkpoint_consistency();
CREATE TRIGGER guard_nfo_ready_checkpoint BEFORE INSERT OR UPDATE ON nfo_write_commit_files_ready
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_ready_checkpoint();
CREATE CONSTRAINT TRIGGER verify_nfo_ready_checkpoint AFTER INSERT OR UPDATE ON nfo_write_commit_files_ready
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_ready_checkpoint();
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
COMMIT;
