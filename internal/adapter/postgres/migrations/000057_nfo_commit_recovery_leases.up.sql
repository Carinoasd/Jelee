BEGIN;
LOCK TABLE jobs,nfo_write_commit_journal,nfo_write_commit_file_plans,nfo_write_commit_files_ready,nfo_write_commit_file_checkpoints,nfo_write_commit_attempt_reservations,nfo_write_commit_attempts,nfo_write_commit_attempt_checkpoints,nfo_write_commit_attempt_ready IN ACCESS EXCLUSIVE MODE;
-- A stopped job keeps its journal (schema49 forbids a new owner or generation),
-- so one bounded recovery lease per job lets a new worker continue the same
-- tokens. Takeover needs an expired lease and advances the epoch; the original
-- job lease cannot act because the job is no longer running.
CREATE TABLE nfo_write_commit_recovery_leases (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE RESTRICT,
 owner text NOT NULL CHECK(octet_length(owner) BETWEEN 1 AND 128),
 epoch bigint NOT NULL CHECK(epoch>0),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at)
);
CREATE FUNCTION guard_nfo_write_commit_recovery_lease() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE job record; live_actor boolean; pending boolean;
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'nfo commit recovery lease is retained' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT kind,state,owner,actor_id FROM %I.jobs WHERE id=$1 FOR UPDATE',TG_TABLE_SCHEMA)
 INTO job USING NEW.job_id;
 IF job.kind IS DISTINCT FROM 'nfo_write' OR job.state NOT IN ('failed','cancelled') OR job.owner IS NOT NULL THEN
  RAISE EXCEPTION 'nfo commit recovery requires a stopped job' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_journal WHERE job_id=$1)',TG_TABLE_SCHEMA)
 INTO pending USING NEW.job_id;
 IF NOT pending THEN
  RAISE EXCEPTION 'nfo commit recovery has no journal' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT is_admin AND NOT disabled AND deleted_at IS NULL FROM %I.users WHERE id=$1 FOR SHARE',TG_TABLE_SCHEMA)
 INTO live_actor USING job.actor_id;
 IF live_actor IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit recovery actor is not active' USING ERRCODE='23514';
 END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.epoch<>1 THEN RAISE EXCEPTION 'nfo commit recovery epoch is invalid' USING ERRCODE='23514'; END IF;
  NEW.recorded_at:=clock_timestamp();
 ELSIF NEW.job_id IS DISTINCT FROM OLD.job_id THEN
  RAISE EXCEPTION 'nfo commit recovery lease is immutable' USING ERRCODE='23514';
 ELSIF NEW IS NOT DISTINCT FROM OLD THEN
  RETURN NEW;
 ELSIF NEW.epoch=OLD.epoch THEN
  -- Only the live holder renews; its first timestamp is kept.
  IF NEW.owner IS DISTINCT FROM OLD.owner OR OLD.lease_until<=clock_timestamp() THEN
   RAISE EXCEPTION 'nfo commit recovery lease is not held' USING ERRCODE='23514';
  END IF;
  NEW.recorded_at:=OLD.recorded_at;
 ELSIF NEW.epoch=OLD.epoch+1 THEN
  IF OLD.lease_until>clock_timestamp() THEN
   RAISE EXCEPTION 'nfo commit recovery lease is still live' USING ERRCODE='23514';
  END IF;
  NEW.recorded_at:=clock_timestamp();
 ELSE
  RAISE EXCEPTION 'nfo commit recovery epoch is invalid' USING ERRCODE='23514';
 END IF;
 IF NEW.lease_until<=clock_timestamp() OR NEW.lease_until>clock_timestamp()+interval '1 hour' THEN
  RAISE EXCEPTION 'nfo commit recovery lease duration is invalid' USING ERRCODE='23514';
 END IF;
 -- Touch the job version so stale snapshots cannot miss a holder change.
 EXECUTE format('UPDATE %I.jobs SET generation=generation WHERE id=$1',TG_TABLE_SCHEMA) USING NEW.job_id;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_nfo_write_commit_recovery_lease BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_recovery_leases
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_commit_recovery_lease();
-- The live lease for a journal: the original running job lease, or a live
-- recovery lease of the same stopped job and generation. NULL means none.
CREATE FUNCTION nfo_commit_live_lease(schema_name text,job uuid,journal_owner text,journal_generation bigint) RETURNS timestamptz
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE current record; recovery timestamptz;
BEGIN
 EXECUTE format('SELECT kind,state,owner,generation,lease_until,cancel_requested FROM %I.jobs WHERE id=$1',schema_name)
 INTO current USING job;
 IF current.kind IS DISTINCT FROM 'nfo_write' OR current.generation IS DISTINCT FROM journal_generation THEN
  RETURN NULL;
 END IF;
 IF current.state='running' THEN
  IF current.owner IS DISTINCT FROM journal_owner OR current.cancel_requested OR current.lease_until IS NULL OR current.lease_until<=clock_timestamp() THEN
   RETURN NULL;
  END IF;
  RETURN current.lease_until;
 END IF;
 IF current.state NOT IN ('failed','cancelled') OR current.owner IS NOT NULL THEN
  RETURN NULL;
 END IF;
 EXECUTE format('SELECT lease_until FROM %I.nfo_write_commit_recovery_leases WHERE job_id=$1 AND lease_until>clock_timestamp() FOR SHARE',schema_name)
 INTO recovery USING job;
 RETURN recovery;
END $$;
CREATE OR REPLACE FUNCTION guard_nfo_commit_files() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
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
CREATE OR REPLACE FUNCTION verify_nfo_commit_files_lease() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE live boolean;
BEGIN
 EXECUTE format('SELECT %I.nfo_commit_live_lease($1,w.job_id,w.owner,w.generation) IS NOT NULL AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL FROM %I.nfo_write_commit_journal w JOIN %I.jobs j ON j.id=w.job_id JOIN %I.users u ON u.id=j.actor_id WHERE w.token=$2',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO live USING TG_TABLE_SCHEMA,NEW.token;
 IF live IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit file lease is not live' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END $$;
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
-- Ownership ignores liveness, as in schema54; the lease guards report expiry.
-- A stopped job with a recovery lease row owns the same token it did before.
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
 EXECUTE format('SELECT j.token FROM %I.nfo_write_commit_journal j JOIN %I.jobs k ON k.id=j.job_id AND k.generation=j.generation WHERE j.job_id=$1 AND j.sequence=$2 AND (k.owner=j.owner OR (k.owner IS NULL AND k.state IN (''failed'',''cancelled'') AND EXISTS(SELECT 1 FROM %I.nfo_write_commit_recovery_leases r WHERE r.job_id=k.id)))',schema_name,schema_name,schema_name)
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
COMMIT;
