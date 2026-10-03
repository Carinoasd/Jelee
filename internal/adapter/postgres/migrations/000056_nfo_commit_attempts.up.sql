BEGIN;
LOCK TABLE jobs,nfo_write_commit_journal,nfo_write_commit_file_plans,nfo_write_commit_files_ready,nfo_write_commit_file_checkpoints IN ACCESS EXCLUSIVE MODE;
CREATE TABLE nfo_commit_attempt_quota_fence (
 slot smallint PRIMARY KEY CHECK(slot=1)
);
INSERT INTO nfo_commit_attempt_quota_fence(slot) VALUES(1);
CREATE TABLE nfo_write_commit_attempt_reservations (
 token uuid PRIMARY KEY REFERENCES nfo_write_commit_file_plans(token) ON DELETE RESTRICT,
 original_bytes bigint NOT NULL CHECK(original_bytes BETWEEN 1 AND 33554432),
 replacement_bytes bigint NOT NULL CHECK(replacement_bytes BETWEEN 1 AND 33554432),
 original_hash bytea NOT NULL CHECK(octet_length(original_hash)=32),
 replacement_hash bytea NOT NULL CHECK(octet_length(replacement_hash)=32),
 retained_bytes bigint NOT NULL CHECK(retained_bytes=4*(3*original_bytes+2*replacement_bytes)),
 initial_attempt smallint NOT NULL DEFAULT 0 CHECK(initial_attempt=0),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at)
);
CREATE TABLE nfo_write_commit_attempts (
 token uuid NOT NULL REFERENCES nfo_write_commit_attempt_reservations(token) ON DELETE RESTRICT,
 attempt smallint NOT NULL CHECK(attempt BETWEEN 0 AND 3),
 previous_attempt smallint,
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at),
 PRIMARY KEY(token,attempt),
 CONSTRAINT nfo_attempt_previous_sequence CHECK((attempt=0 AND previous_attempt IS NULL) OR (attempt>0 AND previous_attempt IS NOT NULL AND previous_attempt=attempt-1)),
 CONSTRAINT nfo_attempt_previous_slot FOREIGN KEY(token,previous_attempt) REFERENCES nfo_write_commit_attempts(token,attempt) ON DELETE RESTRICT
);
ALTER TABLE nfo_write_commit_attempt_reservations ADD CONSTRAINT nfo_attempt_initial_slot
 FOREIGN KEY(token,initial_attempt) REFERENCES nfo_write_commit_attempts(token,attempt)
 DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE nfo_write_commit_attempt_checkpoints (
 token uuid NOT NULL,
 attempt smallint NOT NULL CHECK(attempt BETWEEN 1 AND 3),
 phase smallint NOT NULL CHECK(phase IN(1,2)),
 output_identity bytea NOT NULL CHECK(nfo_commit_identity_valid(output_identity,1)),
 rollback_identity bytea,
 first_phase smallint NOT NULL DEFAULT 1 CHECK(first_phase=1),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at),
 PRIMARY KEY(token,attempt,phase),
 FOREIGN KEY(token,attempt) REFERENCES nfo_write_commit_attempts(token,attempt) ON DELETE RESTRICT,
 UNIQUE(token,attempt,output_identity,phase),
 CONSTRAINT nfo_attempt_first_output FOREIGN KEY(token,attempt,output_identity,first_phase)
  REFERENCES nfo_write_commit_attempt_checkpoints(token,attempt,output_identity,phase) ON DELETE RESTRICT,
 CHECK((phase=1 AND rollback_identity IS NULL) OR
  (phase=2 AND rollback_identity IS NOT NULL AND nfo_commit_identity_valid(rollback_identity,1)
   AND get_byte(output_identity,1)=get_byte(rollback_identity,1) AND output_identity<>rollback_identity))
);
CREATE TABLE nfo_write_commit_attempt_ready (
 token uuid PRIMARY KEY,
 attempt smallint NOT NULL CHECK(attempt BETWEEN 1 AND 3),
 output_identity bytea NOT NULL CHECK(nfo_commit_identity_valid(output_identity,1)),
 rollback_identity bytea NOT NULL CHECK(nfo_commit_identity_valid(rollback_identity,1)),
 complete_phase smallint NOT NULL DEFAULT 2 CHECK(complete_phase=2),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at),
 FOREIGN KEY(token,attempt,output_identity,complete_phase)
  REFERENCES nfo_write_commit_attempt_checkpoints(token,attempt,output_identity,phase) ON DELETE RESTRICT,
 CHECK(get_byte(output_identity,1)=get_byte(rollback_identity,1) AND output_identity<>rollback_identity)
);
-- Budget history from immutable job payload only; do not inspect or adopt files.
-- Copy first timestamps even for stopped jobs. This is retention, not live power.
INSERT INTO nfo_write_commit_attempt_reservations(token,original_bytes,replacement_bytes,original_hash,replacement_hash,retained_bytes,recorded_at,lease_until)
 SELECT p.token,octet_length(e.original_bytes),octet_length(e.replacement_bytes),decode(e.original_sha256,'hex'),decode(e.replacement_sha256,'hex'),
  4*(3*octet_length(e.original_bytes)::bigint+2*octet_length(e.replacement_bytes)),p.recorded_at,p.lease_until
 FROM nfo_write_commit_file_plans p JOIN nfo_write_commit_journal w ON w.token=p.token
 JOIN nfo_write_entries e ON e.job_id=w.job_id AND e.sequence=w.sequence;
INSERT INTO nfo_write_commit_attempts(token,attempt,recorded_at,lease_until)
 SELECT token,0,recorded_at,lease_until FROM nfo_write_commit_attempt_reservations;
DO $$
BEGIN
 IF (SELECT count(*) FROM nfo_write_commit_attempt_reservations)>256
  OR (SELECT COALESCE(sum(retained_bytes),0) FROM nfo_write_commit_attempt_reservations)>1073741824 THEN
  RAISE EXCEPTION 'nfo attempt historical capacity reached' USING ERRCODE='23514';
 END IF;
END $$;
CREATE FUNCTION fence_nfo_commit_attempt_quota() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE affected bigint;
BEGIN
 EXECUTE format('UPDATE %I.nfo_commit_attempt_quota_fence SET slot=slot WHERE slot=1',TG_TABLE_SCHEMA);
 GET DIAGNOSTICS affected=ROW_COUNT;
 IF affected<>1 THEN RAISE EXCEPTION 'nfo attempt quota fence missing' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION retain_nfo_commit_attempt_quota_fence() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' OR (TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'nfo attempt quota fence retained' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER retain_nfo_commit_attempt_quota_fence BEFORE UPDATE OR DELETE ON nfo_commit_attempt_quota_fence
 FOR EACH ROW EXECUTE FUNCTION retain_nfo_commit_attempt_quota_fence();
CREATE TRIGGER aa_fence_nfo_commit_attempt_quota BEFORE INSERT OR UPDATE ON nfo_write_commit_attempt_reservations
 FOR EACH ROW EXECUTE FUNCTION fence_nfo_commit_attempt_quota();

CREATE FUNCTION guard_nfo_commit_attempt() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE scope record; active boolean; payload record; plan record; latest integer; selected record; rows_count bigint; total_bytes bigint; matched bigint; conflict boolean;
BEGIN
 IF TG_OP='DELETE' OR (TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'nfo attempt evidence is immutable' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT j.id,j.kind,j.state,j.owner,j.generation,j.lease_until,j.cancel_requested,j.actor_id,w.owner AS first_owner,w.generation AS first_generation FROM %I.nfo_write_commit_journal w JOIN %I.jobs j ON j.id=w.job_id WHERE w.token=$1 FOR UPDATE OF j',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO scope USING NEW.token;
 IF scope.id IS NULL OR scope.kind IS DISTINCT FROM 'nfo_write' OR scope.state IS DISTINCT FROM 'running'
  OR scope.owner IS DISTINCT FROM scope.first_owner OR scope.generation IS DISTINCT FROM scope.first_generation
  OR scope.cancel_requested OR scope.lease_until IS NULL OR scope.lease_until<=clock_timestamp() THEN
  RAISE EXCEPTION 'nfo attempt lease is not live' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT is_admin AND NOT disabled AND deleted_at IS NULL FROM %I.users WHERE id=$1 FOR SHARE',TG_TABLE_SCHEMA)
 INTO active USING scope.actor_id;
 IF active IS DISTINCT FROM true THEN RAISE EXCEPTION 'nfo attempt actor is not active' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='nfo_write_commit_attempt_reservations' THEN
  EXECUTE format('SELECT octet_length(e.original_bytes)::bigint AS original_bytes,octet_length(e.replacement_bytes)::bigint AS replacement_bytes,decode(e.original_sha256,''hex'') AS original_hash,decode(e.replacement_sha256,''hex'') AS replacement_hash FROM %I.nfo_write_entries e JOIN %I.nfo_write_commit_journal w ON w.job_id=e.job_id AND w.sequence=e.sequence WHERE w.token=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
  INTO payload USING NEW.token;
  IF payload.original_bytes IS DISTINCT FROM NEW.original_bytes OR payload.replacement_bytes IS DISTINCT FROM NEW.replacement_bytes
   OR payload.original_hash IS DISTINCT FROM NEW.original_hash OR payload.replacement_hash IS DISTINCT FROM NEW.replacement_hash THEN
   RAISE EXCEPTION 'nfo attempt reservation differs from intent' USING ERRCODE='23514';
  END IF;
  EXECUTE format('SELECT count(*),COALESCE(sum(retained_bytes),0) FROM %I.nfo_write_commit_attempt_reservations WHERE token<>$1',TG_TABLE_SCHEMA)
  INTO rows_count,total_bytes USING NEW.token;
  IF rows_count>=256 OR total_bytes+NEW.retained_bytes>1073741824 THEN
   RAISE EXCEPTION 'nfo attempt capacity reached' USING ERRCODE='23514';
  END IF;
 ELSE
  EXECUTE format('SELECT max(attempt) FROM %I.nfo_write_commit_attempts WHERE token=$1',TG_TABLE_SCHEMA)
  INTO latest USING NEW.token;
  EXECUTE format('SELECT attempt,output_identity,rollback_identity FROM %I.nfo_write_commit_attempt_ready WHERE token=$1',TG_TABLE_SCHEMA)
  INTO selected USING NEW.token;
  IF TG_TABLE_NAME='nfo_write_commit_attempts' THEN
   IF TG_OP='INSERT' AND NEW.attempt>0 THEN
    EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_files_ready WHERE token=$1)',TG_TABLE_SCHEMA)
    INTO conflict USING NEW.token;
    IF conflict THEN RAISE EXCEPTION 'nfo legacy ready already retained' USING ERRCODE='23514'; END IF;
   END IF;
   IF TG_OP='INSERT' AND NEW.attempt>0 THEN
    EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempt_checkpoints c WHERE c.token=$1 AND c.attempt<$2 AND %I.nfo_commit_xmin_is_current(c.xmin))',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
    INTO conflict USING NEW.token,NEW.attempt;
    IF conflict THEN RAISE EXCEPTION 'nfo attempt phase changed before transaction commit' USING ERRCODE='23514'; END IF;
   END IF;
   IF selected.attempt IS NOT NULL THEN
    EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempts WHERE token=$1 AND attempt=$2)',TG_TABLE_SCHEMA)
    INTO conflict USING NEW.token,NEW.attempt;
    IF conflict IS DISTINCT FROM true THEN RAISE EXCEPTION 'nfo attempt already ready' USING ERRCODE='23514'; END IF;
   END IF;
  ELSE
   IF NEW.attempt IS DISTINCT FROM latest THEN RAISE EXCEPTION 'nfo attempt is not latest' USING ERRCODE='23514'; END IF;
   EXECUTE format('SELECT target_identity FROM %I.nfo_write_commit_file_plans WHERE token=$1',TG_TABLE_SCHEMA)
   INTO plan USING NEW.token;
   IF plan.target_identity IS NULL OR get_byte(plan.target_identity,1)<>get_byte(NEW.output_identity,1)
    OR NEW.output_identity=plan.target_identity OR NEW.rollback_identity=plan.target_identity THEN
    RAISE EXCEPTION 'nfo attempt identity does not match plan' USING ERRCODE='23514';
   END IF;
   EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempt_checkpoints WHERE token=$1 AND attempt<>$2 AND (output_identity=$3 OR output_identity=$4 OR rollback_identity=$3 OR rollback_identity=$4))',TG_TABLE_SCHEMA)
   INTO conflict USING NEW.token,NEW.attempt,NEW.output_identity,NEW.rollback_identity;
   IF NOT conflict THEN
    EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_file_checkpoints WHERE token=$1 AND (output_identity=$2 OR output_identity=$3 OR rollback_identity=$2 OR rollback_identity=$3))',TG_TABLE_SCHEMA)
    INTO conflict USING NEW.token,NEW.output_identity,NEW.rollback_identity;
   END IF;
   IF conflict THEN RAISE EXCEPTION 'nfo attempt identity belongs to another attempt' USING ERRCODE='23514'; END IF;
   IF selected.attempt IS NOT NULL AND (selected.attempt IS DISTINCT FROM NEW.attempt OR selected.output_identity IS DISTINCT FROM NEW.output_identity
    OR (NEW.rollback_identity IS NOT NULL AND selected.rollback_identity IS DISTINCT FROM NEW.rollback_identity)) THEN
    RAISE EXCEPTION 'nfo attempt differs from ready' USING ERRCODE='23514';
   END IF;
   IF TG_TABLE_NAME='nfo_write_commit_attempt_ready' THEN
    EXECUTE format('SELECT count(*) FROM %I.nfo_write_commit_attempt_checkpoints WHERE token=$1 AND attempt=$2 AND output_identity=$3 AND (phase=1 OR rollback_identity=$4)',TG_TABLE_SCHEMA)
    INTO matched USING NEW.token,NEW.attempt,NEW.output_identity,NEW.rollback_identity;
    IF matched<>2 THEN RAISE EXCEPTION 'nfo attempt ready incomplete or conflicting' USING ERRCODE='23514'; END IF;
   END IF;
  END IF;
 END IF;
 EXECUTE format('UPDATE %I.jobs SET generation=generation WHERE id=$1',TG_TABLE_SCHEMA) USING scope.id;
 IF TG_OP='INSERT' THEN NEW.recorded_at:=clock_timestamp();NEW.lease_until:=scope.lease_until; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION seed_nfo_commit_legacy_attempt() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
BEGIN
 EXECUTE format('INSERT INTO %I.nfo_write_commit_attempts(token,attempt) VALUES($1,0)',TG_TABLE_SCHEMA) USING NEW.token;
 RETURN NEW;
END $$;
CREATE TRIGGER seed_nfo_commit_legacy_attempt AFTER INSERT ON nfo_write_commit_attempt_reservations
 FOR EACH ROW EXECUTE FUNCTION seed_nfo_commit_legacy_attempt();
CREATE TRIGGER guard_nfo_write_commit_attempt_reservations BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_attempt_reservations
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_attempt();
CREATE TRIGGER catalog_nfo_write_commit_attempt_reservations BEFORE INSERT OR UPDATE ON nfo_write_commit_attempt_reservations
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE CONSTRAINT TRIGGER lease_nfo_write_commit_attempt_reservations AFTER INSERT OR UPDATE ON nfo_write_commit_attempt_reservations
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_commit_files_lease();
CREATE CONSTRAINT TRIGGER verify_catalog_nfo_write_commit_attempt_reservations AFTER INSERT OR UPDATE ON nfo_write_commit_attempt_reservations
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE TRIGGER guard_nfo_write_commit_attempts BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_attempts
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_attempt();
CREATE TRIGGER catalog_nfo_write_commit_attempts BEFORE INSERT OR UPDATE ON nfo_write_commit_attempts
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE CONSTRAINT TRIGGER lease_nfo_write_commit_attempts AFTER INSERT OR UPDATE ON nfo_write_commit_attempts
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_commit_files_lease();
CREATE CONSTRAINT TRIGGER verify_catalog_nfo_write_commit_attempts AFTER INSERT OR UPDATE ON nfo_write_commit_attempts
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE TRIGGER guard_nfo_write_commit_attempt_checkpoints BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_attempt_checkpoints
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_attempt();
CREATE TRIGGER catalog_nfo_write_commit_attempt_checkpoints BEFORE INSERT OR UPDATE ON nfo_write_commit_attempt_checkpoints
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE CONSTRAINT TRIGGER lease_nfo_write_commit_attempt_checkpoints AFTER INSERT OR UPDATE ON nfo_write_commit_attempt_checkpoints
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_commit_files_lease();
CREATE CONSTRAINT TRIGGER verify_catalog_nfo_write_commit_attempt_checkpoints AFTER INSERT OR UPDATE ON nfo_write_commit_attempt_checkpoints
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE TRIGGER guard_nfo_write_commit_attempt_ready BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_attempt_ready
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_attempt();
CREATE TRIGGER catalog_nfo_write_commit_attempt_ready BEFORE INSERT OR UPDATE ON nfo_write_commit_attempt_ready
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE CONSTRAINT TRIGGER lease_nfo_write_commit_attempt_ready AFTER INSERT OR UPDATE ON nfo_write_commit_attempt_ready
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_commit_files_lease();
CREATE CONSTRAINT TRIGGER verify_catalog_nfo_write_commit_attempt_ready AFTER INSERT OR UPDATE ON nfo_write_commit_attempt_ready
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_catalog();
-- New legacy plans also reserve before their first side effect. They cannot
-- evade the global budget by continuing through the older Stage interface.
CREATE TRIGGER aa_fence_nfo_commit_plan_attempt_quota BEFORE INSERT OR UPDATE ON nfo_write_commit_file_plans
 FOR EACH ROW EXECUTE FUNCTION fence_nfo_commit_attempt_quota();
CREATE FUNCTION reserve_nfo_commit_plan_attempts() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
BEGIN
 EXECUTE format('INSERT INTO %I.nfo_write_commit_attempt_reservations(token,original_bytes,replacement_bytes,original_hash,replacement_hash,retained_bytes) SELECT $1,octet_length(e.original_bytes),octet_length(e.replacement_bytes),decode(e.original_sha256,''hex''),decode(e.replacement_sha256,''hex''),4*(3*octet_length(e.original_bytes)::bigint+2*octet_length(e.replacement_bytes)) FROM %I.nfo_write_entries e JOIN %I.nfo_write_commit_journal w ON w.job_id=e.job_id AND w.sequence=e.sequence WHERE w.token=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 USING NEW.token;
 RETURN NEW;
END $$;
CREATE TRIGGER reserve_nfo_commit_plan_attempts AFTER INSERT ON nfo_write_commit_file_plans
 FOR EACH ROW EXECUTE FUNCTION reserve_nfo_commit_plan_attempts();
CREATE FUNCTION guard_nfo_legacy_attempt_evidence() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE allocated boolean;
BEGIN
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempts WHERE token=$1 AND attempt>0)',TG_TABLE_SCHEMA)
 INTO allocated USING NEW.token;
 IF allocated THEN RAISE EXCEPTION 'nfo legacy evidence conflicts with allocated attempt' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER aa_guard_nfo_legacy_attempt_ready BEFORE INSERT OR UPDATE ON nfo_write_commit_files_ready
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_legacy_attempt_evidence();
CREATE TRIGGER aa_guard_nfo_legacy_attempt_checkpoint BEFORE INSERT OR UPDATE ON nfo_write_commit_file_checkpoints
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_legacy_attempt_evidence();
CREATE FUNCTION verify_nfo_commit_attempt() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE latest integer; matched bigint;
BEGIN
 IF TG_TABLE_NAME='nfo_write_commit_attempt_checkpoints' OR TG_TABLE_NAME='nfo_write_commit_attempt_ready' THEN
  EXECUTE format('SELECT max(attempt) FROM %I.nfo_write_commit_attempts WHERE token=$1',TG_TABLE_SCHEMA)
  INTO latest USING NEW.token;
  IF NEW.attempt IS DISTINCT FROM latest THEN RAISE EXCEPTION 'nfo attempt is not latest' USING ERRCODE='23514'; END IF;
 END IF;
 IF TG_TABLE_NAME='nfo_write_commit_attempt_ready' THEN
  EXECUTE format('SELECT count(*) FROM %I.nfo_write_commit_attempt_checkpoints WHERE token=$1 AND attempt=$2 AND output_identity=$3 AND (phase=1 OR rollback_identity=$4)',TG_TABLE_SCHEMA)
  INTO matched USING NEW.token,NEW.attempt,NEW.output_identity,NEW.rollback_identity;
  IF matched<>2 THEN RAISE EXCEPTION 'nfo attempt ready incomplete or conflicting' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER verify_nfo_write_commit_attempt_checkpoints AFTER INSERT OR UPDATE ON nfo_write_commit_attempt_checkpoints
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_commit_attempt();
CREATE CONSTRAINT TRIGGER verify_nfo_write_commit_attempt_ready AFTER INSERT OR UPDATE ON nfo_write_commit_attempt_ready
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_commit_attempt();
CREATE OR REPLACE FUNCTION guard_nfo_catalog_mutation() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE old_id uuid; new_id uuid; column_name text; entry record;
BEGIN
 IF TG_OP<>'INSERT' THEN old_id:=OLD.id; END IF;
 IF TG_OP<>'DELETE' THEN new_id:=NEW.id; END IF;
 IF TG_TABLE_NAME='items' THEN column_name:='item_id';
 ELSIF TG_TABLE_NAME='libraries' THEN column_name:='library_id';
 ELSE column_name:='root_id'; END IF;
 FOR entry IN EXECUTE format('SELECT DISTINCT e.job_id,e.sequence FROM %I.nfo_write_entries e JOIN %I.nfo_write_commit_journal w ON w.job_id=e.job_id AND w.sequence=e.sequence LEFT JOIN %I.nfo_write_commit_file_plans p ON p.token=w.token LEFT JOIN %I.nfo_write_commit_files_ready r ON r.token=w.token WHERE (e.%I=$1 OR e.%I=$2) AND (%I.nfo_commit_xmin_is_current(w.xmin) OR %I.nfo_commit_xmin_is_current(p.xmin) OR %I.nfo_commit_xmin_is_current(r.xmin) OR EXISTS(SELECT 1 FROM %I.nfo_write_commit_file_checkpoints c WHERE c.token=w.token AND %I.nfo_commit_xmin_is_current(c.xmin)) OR EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempt_reservations a WHERE a.token=w.token AND %I.nfo_commit_xmin_is_current(a.xmin)) OR EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempts a WHERE a.token=w.token AND %I.nfo_commit_xmin_is_current(a.xmin)) OR EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempt_checkpoints a WHERE a.token=w.token AND %I.nfo_commit_xmin_is_current(a.xmin)) OR EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempt_ready a WHERE a.token=w.token AND %I.nfo_commit_xmin_is_current(a.xmin)))',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,column_name,column_name,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 USING old_id,new_id LOOP
  EXECUTE format('SELECT %I.check_nfo_commit_catalog($1,$2,$3)',TG_TABLE_SCHEMA) USING TG_TABLE_SCHEMA,entry.job_id,entry.sequence;
 END LOOP;
 RETURN NULL;
END $$;
COMMIT;
