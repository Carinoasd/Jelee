BEGIN;
LOCK TABLE jobs,nfo_write_preparations,nfo_write_entries,nfo_write_commit_journal,nfo_write_commit_file_plans,nfo_write_commit_file_checkpoints,nfo_write_commit_files_ready,nfo_write_commit_attempt_reservations,nfo_write_commit_attempt_checkpoints,nfo_write_commit_attempt_ready,nfo_write_native_claims IN ACCESS EXCLUSIVE MODE;
-- Created targets are retained state: an older binary has no way to settle a
-- plan without a target identity, read a ready without a rollback object, or
-- release an absent-target claim.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_preparations WHERE create_target)
  OR EXISTS(SELECT 1 FROM nfo_write_entries WHERE create_target)
  OR EXISTS(SELECT 1 FROM nfo_write_commit_file_plans WHERE version=2 OR target_identity IS NULL)
  OR EXISTS(SELECT 1 FROM nfo_write_commit_files_ready WHERE rollback_identity IS NULL)
  OR EXISTS(SELECT 1 FROM nfo_write_commit_attempt_ready WHERE rollback_identity IS NULL)
  OR EXISTS(SELECT 1 FROM nfo_write_commit_attempt_reservations WHERE original_bytes=0)
  OR EXISTS(SELECT 1 FROM nfo_write_native_claims WHERE kind='nfo_absent') THEN
  RAISE EXCEPTION 'retained nfo create state prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP FUNCTION check_nfo_commit_catalog(text,uuid,integer);
ALTER FUNCTION check_nfo_commit_catalog_v87(text,uuid,integer) RENAME TO check_nfo_commit_catalog;
DROP FUNCTION check_nfo_create_claim_scope(text,uuid,integer);
DROP TRIGGER claim_nfo_create_output_files ON nfo_write_commit_files_ready;
DROP TRIGGER claim_nfo_create_output_attempt ON nfo_write_commit_attempt_ready;
DROP FUNCTION claim_nfo_create_output();
CREATE OR REPLACE FUNCTION nfo_write_native_receipt_valid(v bytea)
 RETURNS boolean
 LANGUAGE plpgsql
 IMMUTABLE PARALLEL SAFE STRICT
 SECURITY INVOKER SET search_path=pg_catalog
AS $$
DECLARE count_ancestors integer; i integer; kind integer; r bytea;
BEGIN
 IF octet_length(v)<200 OR octet_length(v)>6296 THEN RETURN false; END IF;
 IF get_byte(v,0)<>1 OR get_byte(v,1) NOT IN(1,2) OR get_byte(v,2) NOT IN(1,2)
 OR get_byte(v,3)<>0 OR get_byte(v,6)<>0 OR get_byte(v,7)<>0 THEN RETURN false; END IF;
 count_ancestors:=get_byte(v,4)+256*get_byte(v,5);
 IF count_ancestors<1 OR count_ancestors>128 OR octet_length(v)<>152+48*count_ancestors THEN RETURN false; END IF;
 FOR i IN 0..count_ancestors+2 LOOP
  r:=substring(v FROM 9+48*i FOR 48);
  kind:=CASE WHEN i=1 THEN get_byte(v,2) WHEN i=2 THEN 1 ELSE 2 END;
  IF get_byte(r,0)<>1 OR get_byte(r,1)<>get_byte(v,1) OR get_byte(r,2)<>kind
  OR substring(r FROM 4 FOR 5)<>decode('0000000000','hex')
  OR substring(r FROM 45 FOR 4)<>decode('00000000','hex') THEN RETURN false; END IF;
  IF get_byte(v,1)=1 THEN
   IF substring(r FROM 41 FOR 4)<>decode('00000000','hex') THEN RETURN false; END IF;
  ELSE
   IF substring(r FROM 25 FOR 8)<>decode('0000000000000000','hex')
   OR get_byte(r,40)::bigint+256*get_byte(r,41)::bigint+65536*get_byte(r,42)::bigint+16777216*get_byte(r,43)::bigint>=1000000000 THEN RETURN false; END IF;
  END IF;
 END LOOP;
 IF get_byte(v,2)=1 AND substring(v FROM 57 FOR 48)=substring(v FROM 105 FOR 48) THEN RETURN false; END IF;
 RETURN true;
END $$;
CREATE OR REPLACE FUNCTION guard_nfo_commit_plan_native()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY INVOKER SET search_path=pg_catalog
AS $$
DECLARE retained bytea; valid boolean;
BEGIN
 EXECUTE format('SELECT e.native_receipt FROM %I.nfo_write_commit_journal j JOIN %I.nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE j.token=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO retained USING NEW.token;
 EXECUTE format('SELECT %I.nfo_write_native_receipt_valid($1)',TG_TABLE_SCHEMA) INTO valid USING retained;
 IF valid IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit native receipt missing or invalid' USING ERRCODE='23514';
 END IF;
 IF NEW.parent_identity IS DISTINCT FROM substring(retained FROM octet_length(retained)-47 FOR 48)
 OR NEW.target_identity IS DISTINCT FROM substring(retained FROM 105 FOR 48) THEN
  RAISE EXCEPTION 'nfo commit plan differs from first native observation' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION guard_nfo_commit_files()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY INVOKER SET search_path=pg_catalog
AS $$
DECLARE live_until timestamptz; scope record; live_actor boolean; plan record;
BEGIN
 IF TG_OP='DELETE' OR TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'nfo commit file evidence is immutable' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT j.id,j.kind,j.state,j.owner,j.generation,j.lease_until,j.cancel_requested,j.actor_id,w.owner AS journal_owner,w.generation AS journal_generation,e.relative_path FROM %I.nfo_write_commit_journal w JOIN %I.jobs j ON j.id=w.job_id JOIN %I.nfo_write_entries e ON e.job_id=w.job_id AND e.sequence=w.sequence WHERE w.token=$1 FOR UPDATE OF j',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO scope USING NEW.token;
 IF scope.id IS NOT NULL THEN
  EXECUTE format('SELECT %I.nfo_commit_live_lease($1,$2,$3,$4)',TG_TABLE_SCHEMA) INTO live_until
  USING TG_TABLE_SCHEMA,scope.id,scope.journal_owner,scope.journal_generation;
 END IF;
 IF scope.id IS NULL OR live_until IS NULL THEN
  RAISE EXCEPTION 'nfo commit file lease is not live' USING ERRCODE='23514';
 END IF;
 -- A cancelled job is only settled or rolled back; recovery adds no new evidence.
 IF TG_OP='INSERT' AND scope.state<>'running' AND scope.cancel_requested THEN
  RAISE EXCEPTION 'nfo commit recovery cannot advance a cancelled job' USING ERRCODE='23514';
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
  NEW.recorded_at:=clock_timestamp(); NEW.lease_until:=live_until;
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION guard_nfo_ready_checkpoint()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY INVOKER SET search_path=pg_catalog
AS $$
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
CREATE OR REPLACE FUNCTION guard_nfo_commit_attempt()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY INVOKER SET search_path=pg_catalog
AS $$
DECLARE live_until timestamptz; scope record; active boolean; payload record; plan record; latest integer; selected record; rows_count bigint; total_bytes bigint; matched bigint; conflict boolean;
BEGIN
 IF TG_OP='DELETE' OR (TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'nfo attempt evidence is immutable' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT j.id,j.kind,j.state,j.owner,j.generation,j.lease_until,j.cancel_requested,j.actor_id,w.owner AS first_owner,w.generation AS first_generation FROM %I.nfo_write_commit_journal w JOIN %I.jobs j ON j.id=w.job_id WHERE w.token=$1 FOR UPDATE OF j',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO scope USING NEW.token;
 IF scope.id IS NOT NULL THEN
  EXECUTE format('SELECT %I.nfo_commit_live_lease($1,$2,$3,$4)',TG_TABLE_SCHEMA) INTO live_until
  USING TG_TABLE_SCHEMA,scope.id,scope.first_owner,scope.first_generation;
 END IF;
 IF scope.id IS NULL OR live_until IS NULL THEN
  RAISE EXCEPTION 'nfo attempt lease is not live' USING ERRCODE='23514';
 END IF;
 -- A cancelled job is only settled or rolled back; recovery adds no new evidence.
 IF TG_OP='INSERT' AND scope.state<>'running' AND scope.cancel_requested THEN
  RAISE EXCEPTION 'nfo commit recovery cannot advance a cancelled job' USING ERRCODE='23514';
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
  -- A cleaned token retains no files, so its reservation no longer counts.
  EXECUTE format('SELECT count(*),COALESCE(sum(r.retained_bytes),0) FROM %I.nfo_write_commit_attempt_reservations r WHERE r.token<>$1 AND NOT EXISTS(SELECT 1 FROM %I.nfo_write_commit_cleanups c WHERE c.token=r.token)',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
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
 IF TG_OP='INSERT' THEN NEW.recorded_at:=clock_timestamp();NEW.lease_until:=live_until; END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION verify_nfo_commit_attempt()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY INVOKER SET search_path=pg_catalog
AS $$
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
CREATE OR REPLACE FUNCTION record_nfo_native_claims()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY INVOKER SET search_path=pg_catalog
AS $$
BEGIN
 EXECUTE format('INSERT INTO %I.nfo_write_native_claims(kind,identity,token) SELECT ''media'',substring(e.native_receipt FROM 57 FOR 48),$1 FROM %I.nfo_write_entries e WHERE e.job_id=$2 AND e.sequence=$3 UNION ALL SELECT ''nfo'',substring(e.native_receipt FROM 105 FOR 48),$1 FROM %I.nfo_write_entries e WHERE e.job_id=$2 AND e.sequence=$3',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 USING NEW.token,NEW.job_id,NEW.sequence;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION guard_nfo_native_claim()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY INVOKER SET search_path=pg_catalog
AS $$
DECLARE retained bytea; valid boolean; expected bytea; resolved boolean;
BEGIN
 IF TG_OP='DELETE' THEN
  EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_journal w JOIN %I.nfo_write_commit_resolutions r ON r.job_id=w.job_id WHERE w.token=$1)',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
  INTO resolved USING OLD.token;
  IF resolved THEN RETURN OLD; END IF;
  RAISE EXCEPTION 'nfo native claim is immutable' USING ERRCODE='23514';
 END IF;
 IF TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'nfo native claim is immutable' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT e.native_receipt FROM %I.nfo_write_commit_journal j JOIN %I.nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE j.token=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO retained USING NEW.token;
 EXECUTE format('SELECT %I.nfo_write_native_receipt_valid($1)',TG_TABLE_SCHEMA) INTO valid USING retained;
 IF valid IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo native claim first observation missing' USING ERRCODE='23514';
 END IF;
 IF NEW.kind='nfo' THEN expected:=substring(retained FROM 105 FOR 48);
 ELSIF NEW.kind='media' THEN expected:=substring(retained FROM 57 FOR 48);
 ELSE RAISE EXCEPTION 'nfo native claim kind invalid' USING ERRCODE='23514'; END IF;
 IF NEW.identity IS DISTINCT FROM expected THEN
  RAISE EXCEPTION 'nfo native claim differs from first observation' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION verify_nfo_native_claims()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY INVOKER SET search_path=pg_catalog
AS $$
DECLARE observed bigint;
BEGIN
 EXECUTE format('SELECT count(*) FROM %I.nfo_write_native_claims WHERE token=$1',TG_TABLE_SCHEMA)
 INTO observed USING NEW.token;
 IF observed<>2 THEN RAISE EXCEPTION 'nfo physical claims incomplete' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
DROP FUNCTION nfo_absent_target_key(bytea,text);
ALTER TABLE nfo_write_native_claims DROP CONSTRAINT nfo_write_native_claims_absent_key;
ALTER TABLE nfo_write_native_claims DROP CONSTRAINT nfo_write_native_claims_kind_check;
ALTER TABLE nfo_write_native_claims ADD CONSTRAINT nfo_write_native_claims_kind_check CHECK(kind IN ('nfo','media'));
ALTER TABLE nfo_write_commit_attempt_reservations DROP CONSTRAINT nfo_write_commit_attempt_reservations_original_bytes_check;
ALTER TABLE nfo_write_commit_attempt_reservations ADD CONSTRAINT nfo_write_commit_attempt_reservations_original_bytes_check CHECK(original_bytes BETWEEN 1 AND 33554432);
ALTER TABLE nfo_write_commit_attempt_ready DROP CONSTRAINT nfo_write_commit_attempt_ready_complete_phase_check;
ALTER TABLE nfo_write_commit_attempt_ready ADD CONSTRAINT nfo_write_commit_attempt_ready_complete_phase_check CHECK(complete_phase=2);
ALTER TABLE nfo_write_commit_attempt_ready ALTER COLUMN rollback_identity SET NOT NULL;
ALTER TABLE nfo_write_commit_files_ready ALTER COLUMN rollback_identity SET NOT NULL;
ALTER TABLE nfo_write_commit_file_plans DROP CONSTRAINT nfo_write_commit_file_plans_version_check;
ALTER TABLE nfo_write_commit_file_plans ADD CONSTRAINT nfo_write_commit_file_plans_version_check CHECK(version=1);
ALTER TABLE nfo_write_commit_file_plans ALTER COLUMN target_identity SET NOT NULL;
ALTER TABLE nfo_write_entries DROP CONSTRAINT nfo_write_entries_create_target;
ALTER TABLE nfo_write_preparations DROP CONSTRAINT nfo_write_preparations_create_target;
ALTER TABLE nfo_write_entries DROP CONSTRAINT nfo_write_entries_original_size;
ALTER TABLE nfo_write_preparations DROP CONSTRAINT nfo_write_preparations_original_size;
ALTER TABLE nfo_write_entries ADD CONSTRAINT nfo_write_preparations_check1 CHECK(octet_length(original_bytes) BETWEEN 1 AND max_bytes);
ALTER TABLE nfo_write_preparations ADD CONSTRAINT nfo_write_preparations_check1 CHECK(octet_length(original_bytes) BETWEEN 1 AND max_bytes);
ALTER TABLE nfo_write_entries DROP COLUMN create_target;
ALTER TABLE nfo_write_preparations DROP COLUMN create_target;
COMMIT;
