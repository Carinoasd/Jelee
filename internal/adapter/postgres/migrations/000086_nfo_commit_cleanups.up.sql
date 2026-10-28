BEGIN;
LOCK TABLE jobs,nfo_write_commit_journal,nfo_write_commit_attempt_reservations,nfo_write_commit_resolutions,nfo_commit_attempt_quota_fence IN ACCESS EXCLUSIVE MODE;
-- Cleanup removes the files a resolved token retained (schema59 keeps them for
-- recovery) and then releases its attempt reservation. One bounded lease per
-- token makes concurrent cleaners exclusive; an expired lease passes to a new
-- owner with the next epoch, as recovery leases do.
CREATE TABLE nfo_write_commit_cleanup_leases (
 token uuid PRIMARY KEY REFERENCES nfo_write_commit_journal(token) ON DELETE RESTRICT,
 owner text NOT NULL CHECK(octet_length(owner) BETWEEN 1 AND 128),
 epoch bigint NOT NULL CHECK(epoch>0),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at)
);
-- The outcome of one token's cleanup, written once after the filesystem pass:
-- 1 removed every retained object, 2 kept objects that did not match their
-- durable identity (retained counts them), 3 the planned parent directory was
-- gone or replaced. Any row releases the token's reservation.
CREATE TABLE nfo_write_commit_cleanups (
 token uuid PRIMARY KEY REFERENCES nfo_write_commit_cleanup_leases(token) ON DELETE RESTRICT,
 owner text NOT NULL CHECK(octet_length(owner) BETWEEN 1 AND 128),
 epoch bigint NOT NULL CHECK(epoch>0),
 outcome smallint NOT NULL CHECK(outcome BETWEEN 1 AND 3),
 retained smallint NOT NULL CHECK(retained BETWEEN 0 AND 64),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 CHECK((outcome=2)=(retained>0))
);
CREATE FUNCTION guard_nfo_commit_cleanup_lease() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE resolved boolean; cleaned boolean;
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'nfo commit cleanup lease is retained' USING ERRCODE='23514';
 END IF;
 IF TG_OP='UPDATE' AND NEW IS NOT DISTINCT FROM OLD THEN
  RETURN NEW;
 END IF;
 -- A resolved job has no live original or recovery lease, so no writer can
 -- touch this token's files while it is cleaned.
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_journal w JOIN %I.nfo_write_commit_resolutions r ON r.job_id=w.job_id WHERE w.token=$1)',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO resolved USING NEW.token;
 IF resolved IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit cleanup requires a resolved job' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_cleanups WHERE token=$1)',TG_TABLE_SCHEMA)
 INTO cleaned USING NEW.token;
 IF cleaned THEN
  RAISE EXCEPTION 'nfo commit cleanup is already recorded' USING ERRCODE='23514';
 END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.epoch<>1 THEN RAISE EXCEPTION 'nfo commit cleanup epoch is invalid' USING ERRCODE='23514'; END IF;
  NEW.recorded_at:=clock_timestamp();
 ELSIF NEW.token IS DISTINCT FROM OLD.token THEN
  RAISE EXCEPTION 'nfo commit cleanup lease is immutable' USING ERRCODE='23514';
 ELSIF NEW.epoch=OLD.epoch THEN
  -- Only the live holder renews; its first timestamp is kept.
  IF NEW.owner IS DISTINCT FROM OLD.owner OR OLD.lease_until<=clock_timestamp() THEN
   RAISE EXCEPTION 'nfo commit cleanup lease is not held' USING ERRCODE='23514';
  END IF;
  NEW.recorded_at:=OLD.recorded_at;
 ELSIF NEW.epoch=OLD.epoch+1 THEN
  IF OLD.lease_until>clock_timestamp() THEN
   RAISE EXCEPTION 'nfo commit cleanup lease is still live' USING ERRCODE='23514';
  END IF;
  NEW.recorded_at:=clock_timestamp();
 ELSE
  RAISE EXCEPTION 'nfo commit cleanup epoch is invalid' USING ERRCODE='23514';
 END IF;
 IF NEW.lease_until<=clock_timestamp() OR NEW.lease_until>clock_timestamp()+interval '1 hour' THEN
  RAISE EXCEPTION 'nfo commit cleanup lease duration is invalid' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_nfo_write_commit_cleanup_leases BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_cleanup_leases
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_cleanup_lease();
CREATE FUNCTION guard_nfo_commit_cleanup() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE held boolean;
BEGIN
 IF TG_OP='DELETE' OR (TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'nfo commit cleanup is immutable' USING ERRCODE='23514';
 END IF;
 IF TG_OP='UPDATE' THEN RETURN NEW; END IF;
 -- Lock the lease row so a takeover cannot interleave with the record.
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_cleanup_leases WHERE token=$1 AND owner=$2 AND epoch=$3 AND lease_until>clock_timestamp() FOR UPDATE)',TG_TABLE_SCHEMA)
 INTO held USING NEW.token,NEW.owner,NEW.epoch;
 IF held IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit cleanup lease is not held' USING ERRCODE='23514';
 END IF;
 NEW.recorded_at:=clock_timestamp();
 RETURN NEW;
END $$;
-- Releasing a reservation takes the same quota fence as admitting one, so a
-- capacity check never mixes snapshots taken before and after a release.
CREATE TRIGGER aa_fence_nfo_commit_cleanup_quota BEFORE INSERT OR UPDATE ON nfo_write_commit_cleanups
 FOR EACH ROW EXECUTE FUNCTION fence_nfo_commit_attempt_quota();
CREATE TRIGGER guard_nfo_write_commit_cleanups BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_cleanups
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_cleanup();
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
COMMIT;
