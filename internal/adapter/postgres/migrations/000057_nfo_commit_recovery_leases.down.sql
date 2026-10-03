BEGIN;
LOCK TABLE jobs,nfo_write_commit_journal,nfo_write_commit_recovery_leases IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_commit_recovery_leases) OR EXISTS(SELECT 1 FROM nfo_write_commit_journal) THEN
  RAISE EXCEPTION 'nfo commit recovery leases or journal are retained' USING ERRCODE='23514';
 END IF;
END $$;
CREATE OR REPLACE FUNCTION guard_nfo_commit_files() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE scope record; live_actor boolean; plan record;
BEGIN
 IF TG_OP='DELETE' OR TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'nfo commit file evidence is immutable' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT j.id,j.kind,j.state,j.owner,j.generation,j.lease_until,j.cancel_requested,j.actor_id,w.owner AS journal_owner,w.generation AS journal_generation,e.relative_path FROM %I.nfo_write_commit_journal w JOIN %I.jobs j ON j.id=w.job_id JOIN %I.nfo_write_entries e ON e.job_id=w.job_id AND e.sequence=w.sequence WHERE w.token=$1 FOR UPDATE OF j',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO scope USING NEW.token;
 IF scope.id IS NULL OR scope.kind IS DISTINCT FROM 'nfo_write' OR scope.state IS DISTINCT FROM 'running'
  OR scope.owner IS DISTINCT FROM scope.journal_owner OR scope.generation IS DISTINCT FROM scope.journal_generation
  OR scope.cancel_requested OR scope.lease_until IS NULL OR scope.lease_until<=clock_timestamp() THEN
  RAISE EXCEPTION 'nfo commit file lease is not live' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT is_admin AND NOT disabled AND deleted_at IS NULL FROM %I.users WHERE id=$1 FOR SHARE',TG_TABLE_SCHEMA)
 INTO live_actor USING scope.actor_id;
 IF live_actor IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit file actor is not active' USING ERRCODE='23514';
 END IF;
 IF TG_TABLE_NAME='nfo_write_commit_file_plans' THEN
  IF NEW.target_name IS DISTINCT FROM regexp_replace(scope.relative_path,'^.*/','') THEN
   RAISE EXCEPTION 'nfo commit file target does not match intent' USING ERRCODE='23514';
  END IF;
 ELSE
  EXECUTE format('SELECT target_identity FROM %I.nfo_write_commit_file_plans WHERE token=$1',TG_TABLE_SCHEMA)
  INTO plan USING NEW.token;
  IF plan.target_identity IS NULL OR get_byte(plan.target_identity,1)<>get_byte(NEW.output_identity,1)
   OR plan.target_identity=NEW.output_identity OR plan.target_identity=NEW.rollback_identity THEN
   RAISE EXCEPTION 'nfo commit ready identity does not match plan' USING ERRCODE='23514';
  END IF;
 END IF;
 EXECUTE format('UPDATE %I.jobs SET generation=generation WHERE id=$1',TG_TABLE_SCHEMA) USING scope.id;
 IF TG_OP='INSERT' THEN
  NEW.recorded_at:=clock_timestamp(); NEW.lease_until:=scope.lease_until;
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION verify_nfo_commit_files_lease() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE live boolean;
BEGIN
 EXECUTE format('SELECT j.kind=''nfo_write'' AND j.state=''running'' AND j.owner=w.owner AND j.generation=w.generation AND j.lease_until>clock_timestamp() AND NOT j.cancel_requested AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL FROM %I.nfo_write_commit_journal w JOIN %I.jobs j ON j.id=w.job_id JOIN %I.users u ON u.id=j.actor_id WHERE w.token=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO live USING NEW.token;
 IF live IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit file lease is not live' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END $$;
CREATE OR REPLACE FUNCTION guard_nfo_commit_attempt() RETURNS trigger
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
CREATE OR REPLACE FUNCTION check_nfo_native_claim_scope(schema_name text,job uuid,seq integer) RETURNS void
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE retained bytea; owned_token uuid; unknown boolean; conflicting boolean; claims bigint;
BEGIN
 EXECUTE format('SELECT native_receipt FROM %I.nfo_write_entries WHERE job_id=$1 AND sequence=$2',schema_name)
 INTO retained USING job,seq;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_journal j JOIN %I.nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE e.native_receipt IS NULL)',schema_name,schema_name)
 INTO unknown;
 IF unknown THEN
  RAISE EXCEPTION 'nfo historical physical observation unresolved' USING ERRCODE='23505',CONSTRAINT='nfo_native_target_unresolved';
 END IF;
 EXECUTE format('SELECT j.token FROM %I.nfo_write_commit_journal j JOIN %I.jobs k ON k.id=j.job_id AND k.generation=j.generation AND k.owner=j.owner WHERE j.job_id=$1 AND j.sequence=$2',schema_name,schema_name)
 INTO owned_token USING job,seq;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_native_claims WHERE ((identity=substring($1 FROM 105 FOR 48)) OR (identity=substring($1 FROM 57 FOR 48))) AND ($2 IS NULL OR token<>$2))',schema_name)
 INTO conflicting USING retained,owned_token;
 IF conflicting THEN
  RAISE EXCEPTION 'nfo physical target unresolved' USING ERRCODE='23505',CONSTRAINT='nfo_native_target_unresolved';
 END IF;
 IF owned_token IS NOT NULL THEN
  EXECUTE format('SELECT count(*) FROM %I.nfo_write_native_claims WHERE token=$1 AND ((kind=''nfo'' AND identity=substring($2 FROM 105 FOR 48)) OR (kind=''media'' AND identity=substring($2 FROM 57 FOR 48)))',schema_name)
  INTO claims USING owned_token,retained;
  IF claims<>2 THEN
   RAISE EXCEPTION 'nfo physical claims missing' USING ERRCODE='23514';
  END IF;
 END IF;
END $$;
DROP FUNCTION nfo_commit_live_lease(text,uuid,text,bigint);
DROP TABLE nfo_write_commit_recovery_leases;
DROP FUNCTION guard_nfo_write_commit_recovery_lease();
COMMIT;
