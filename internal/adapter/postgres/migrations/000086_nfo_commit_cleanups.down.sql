BEGIN;
LOCK TABLE jobs,nfo_write_commit_journal,nfo_write_commit_attempt_reservations,nfo_write_commit_resolutions,nfo_commit_attempt_quota_fence IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_commit_cleanups) OR EXISTS(SELECT 1 FROM nfo_write_commit_cleanup_leases) THEN
  RAISE EXCEPTION 'nfo commit cleanups are retained' USING ERRCODE='23514';
 END IF;
END $$;
DROP TRIGGER guard_nfo_write_commit_cleanups ON nfo_write_commit_cleanups;
DROP TRIGGER aa_fence_nfo_commit_cleanup_quota ON nfo_write_commit_cleanups;
DROP FUNCTION guard_nfo_commit_cleanup();
DROP TRIGGER guard_nfo_write_commit_cleanup_leases ON nfo_write_commit_cleanup_leases;
DROP FUNCTION guard_nfo_commit_cleanup_lease();
DROP TABLE nfo_write_commit_cleanups;
DROP TABLE nfo_write_commit_cleanup_leases;
CREATE OR REPLACE FUNCTION guard_nfo_commit_attempt() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
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
 IF TG_OP='INSERT' THEN NEW.recorded_at:=clock_timestamp();NEW.lease_until:=live_until; END IF;
 RETURN NEW;
END $$;
COMMIT;
