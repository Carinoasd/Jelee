BEGIN;
LOCK TABLE jobs,nfo_write_preparations,nfo_write_entries,nfo_write_commit_journal,nfo_write_commit_file_plans,nfo_write_commit_file_checkpoints,nfo_write_commit_files_ready,nfo_write_commit_attempt_reservations,nfo_write_commit_attempt_checkpoints,nfo_write_commit_attempt_ready,nfo_write_native_claims IN ACCESS EXCLUSIVE MODE;
-- A create entry targets an NFO name that does not exist. It never overwrites
-- anything: the commit only ever links the staged output to the absent name
-- (link fails when the name exists). Every rewrite CHECK stays as strict as it
-- was; a create row differs only where it has nothing to compare: no original
-- bytes, no target identity and no rollback object.
ALTER TABLE nfo_write_preparations ADD COLUMN create_target boolean NOT NULL DEFAULT false;
ALTER TABLE nfo_write_entries ADD COLUMN create_target boolean NOT NULL DEFAULT false;
ALTER TABLE nfo_write_preparations DROP CONSTRAINT nfo_write_preparations_check1;
ALTER TABLE nfo_write_entries DROP CONSTRAINT nfo_write_preparations_check1;
ALTER TABLE nfo_write_preparations ADD CONSTRAINT nfo_write_preparations_original_size CHECK(CASE WHEN create_target THEN octet_length(original_bytes)=0 ELSE octet_length(original_bytes) BETWEEN 1 AND max_bytes END);
ALTER TABLE nfo_write_entries ADD CONSTRAINT nfo_write_entries_original_size CHECK(CASE WHEN create_target THEN octet_length(original_bytes)=0 ELSE octet_length(original_bytes) BETWEEN 1 AND max_bytes END);
-- The flag, the request and the first observation must agree: the request's
-- create key (omitted when false) and the receipt's target-absent header byte.
ALTER TABLE nfo_write_preparations ADD CONSTRAINT nfo_write_preparations_create_target CHECK(create_target=COALESCE((convert_from(request_bytes,'UTF8')::jsonb->>'create')::boolean,false) AND create_target=COALESCE(get_byte(native_receipt,3)=1,false));
ALTER TABLE nfo_write_entries ADD CONSTRAINT nfo_write_entries_create_target CHECK(create_target=COALESCE((convert_from(request_bytes,'UTF8')::jsonb->>'create')::boolean,false) AND create_target=COALESCE(get_byte(native_receipt,3)=1,false));

-- A plan of version 2 creates a target observed absent: no target identity.
ALTER TABLE nfo_write_commit_file_plans ALTER COLUMN target_identity DROP NOT NULL;
ALTER TABLE nfo_write_commit_file_plans DROP CONSTRAINT nfo_write_commit_file_plans_version_check;
ALTER TABLE nfo_write_commit_file_plans ADD CONSTRAINT nfo_write_commit_file_plans_version_check CHECK((version=1 AND target_identity IS NOT NULL) OR (version=2 AND target_identity IS NULL));
-- Only the output is retained by a create: its ready evidence has no rollback
-- identity, and an attempt ready completes at checkpoint phase 1.
ALTER TABLE nfo_write_commit_files_ready ALTER COLUMN rollback_identity DROP NOT NULL;
ALTER TABLE nfo_write_commit_attempt_ready ALTER COLUMN rollback_identity DROP NOT NULL;
ALTER TABLE nfo_write_commit_attempt_ready DROP CONSTRAINT nfo_write_commit_attempt_ready_complete_phase_check;
ALTER TABLE nfo_write_commit_attempt_ready ADD CONSTRAINT nfo_write_commit_attempt_ready_complete_phase_check CHECK(complete_phase IN (1,2) AND (complete_phase=1)=(rollback_identity IS NULL));
-- A create reservation has no original to charge; the trigger ties the zero to
-- the intent, whose original is empty only for a create entry.
ALTER TABLE nfo_write_commit_attempt_reservations DROP CONSTRAINT nfo_write_commit_attempt_reservations_original_bytes_check;
ALTER TABLE nfo_write_commit_attempt_reservations ADD CONSTRAINT nfo_write_commit_attempt_reservations_original_bytes_check CHECK(original_bytes BETWEEN 0 AND 33554432);

-- The absent-target claim is keyed by a domain-separated SHA-384 of the parent
-- directory identity and the file name, with the first byte forced to 255. No
-- valid native identity starts with 255 (they start with 1), so a claim key can
-- never equal a physical object's identity and no user input forms one.
ALTER TABLE nfo_write_native_claims DROP CONSTRAINT nfo_write_native_claims_kind_check;
ALTER TABLE nfo_write_native_claims ADD CONSTRAINT nfo_write_native_claims_kind_check CHECK(kind IN ('nfo','media','nfo_absent'));
ALTER TABLE nfo_write_native_claims ADD CONSTRAINT nfo_write_native_claims_absent_key CHECK((kind='nfo_absent')=(get_byte(identity,0)=255));
CREATE FUNCTION nfo_absent_target_key(parent bytea,target_name text) RETURNS bytea
 LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SET search_path=pg_catalog AS $$
 SELECT set_byte(sha384(convert_to('jelee/nfo-absent-target/v1','UTF8')||'\x00'::bytea||parent||convert_to(target_name,'UTF8')),0,255)
$$;

-- Header byte 3 is 1 for a create receipt: the NFO record (third) is all zero
-- because the name was observed absent under the last ancestor, its parent.
CREATE OR REPLACE FUNCTION nfo_write_native_receipt_valid(v bytea) RETURNS boolean
 LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE SET search_path=pg_catalog AS $$
DECLARE count_ancestors integer; i integer; kind integer; r bytea; absent boolean;
BEGIN
 IF octet_length(v)<200 OR octet_length(v)>6296 THEN RETURN false; END IF;
 IF get_byte(v,0)<>1 OR get_byte(v,1) NOT IN(1,2) OR get_byte(v,2) NOT IN(1,2)
 OR get_byte(v,3) NOT IN(0,1) OR get_byte(v,6)<>0 OR get_byte(v,7)<>0 THEN RETURN false; END IF;
 absent:=get_byte(v,3)=1;
 count_ancestors:=get_byte(v,4)+256*get_byte(v,5);
 IF count_ancestors<1 OR count_ancestors>128 OR octet_length(v)<>152+48*count_ancestors THEN RETURN false; END IF;
 FOR i IN 0..count_ancestors+2 LOOP
  r:=substring(v FROM 9+48*i FOR 48);
  IF i=2 AND absent THEN
   IF r<>decode(repeat('00',48),'hex') THEN RETURN false; END IF;
   CONTINUE;
  END IF;
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
 IF NOT absent AND get_byte(v,2)=1 AND substring(v FROM 57 FOR 48)=substring(v FROM 105 FOR 48) THEN RETURN false; END IF;
 RETURN true;
END $$;

-- The plan of a create is version 2 with no target identity, and only a create.
CREATE OR REPLACE FUNCTION guard_nfo_commit_plan_native() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE retained bytea; valid boolean; creating boolean;
BEGIN
 EXECUTE format('SELECT e.native_receipt,e.create_target FROM %I.nfo_write_commit_journal j JOIN %I.nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE j.token=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO retained,creating USING NEW.token;
 EXECUTE format('SELECT %I.nfo_write_native_receipt_valid($1)',TG_TABLE_SCHEMA) INTO valid USING retained;
 IF valid IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit native receipt missing or invalid' USING ERRCODE='23514';
 END IF;
 IF NEW.parent_identity IS DISTINCT FROM substring(retained FROM octet_length(retained)-47 FOR 48)
 OR creating AND (NEW.version<>2 OR NEW.target_identity IS NOT NULL)
 OR NOT creating AND (NEW.version<>1 OR NEW.target_identity IS DISTINCT FROM substring(retained FROM 105 FOR 48)) THEN
  RAISE EXCEPTION 'nfo commit plan differs from first native observation' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

-- Ready and checkpoint evidence of a create has no rollback object and a single
-- phase; a rewrite keeps both. A plan must exist, as before.
CREATE OR REPLACE FUNCTION guard_nfo_commit_files() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE live_until timestamptz; scope record; live_actor boolean; plan record; extra_phase boolean:=false;
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
  EXECUTE format('SELECT version,parent_identity,target_identity FROM %I.nfo_write_commit_file_plans WHERE token=$1',TG_TABLE_SCHEMA)
  INTO plan USING NEW.token;
  -- Only the checkpoint table has a phase; a create keeps a single phase.
  IF TG_TABLE_NAME='nfo_write_commit_file_checkpoints' THEN extra_phase:=NEW.phase<>1; END IF;
  IF plan.version IS NULL OR get_byte(plan.parent_identity,1)<>get_byte(NEW.output_identity,1)
   OR plan.target_identity=NEW.output_identity OR plan.target_identity=NEW.rollback_identity
   OR plan.version=1 AND TG_TABLE_NAME='nfo_write_commit_files_ready' AND NEW.rollback_identity IS NULL
   OR plan.version=2 AND (NEW.rollback_identity IS NOT NULL OR extra_phase) THEN
   RAISE EXCEPTION 'nfo commit ready identity does not match plan' USING ERRCODE='23514';
  END IF;
 END IF;
 EXECUTE format('UPDATE %I.jobs SET generation=generation WHERE id=$1',TG_TABLE_SCHEMA) USING scope.id;
 IF TG_OP='INSERT' THEN
  NEW.recorded_at:=clock_timestamp(); NEW.lease_until:=live_until;
 END IF;
 RETURN NEW;
END $$;

-- A create ready is complete with its single output checkpoint (no phase 2).
CREATE OR REPLACE FUNCTION guard_nfo_ready_checkpoint() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE phases bigint; matched bigint;
BEGIN
 EXECUTE format('SELECT count(*),count(*) FILTER(WHERE output_identity=$2 AND (phase=1 OR rollback_identity=$3)) FROM %I.nfo_write_commit_file_checkpoints WHERE token=$1',TG_TABLE_SCHEMA)
 INTO phases,matched USING NEW.token,NEW.output_identity,NEW.rollback_identity;
 IF NEW.rollback_identity IS NULL THEN
  IF phases<>1 OR matched<>1 THEN
   RAISE EXCEPTION 'nfo ready checkpoint incomplete or conflicting' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 -- Ready-only historical callers keep their first complete observation. Once
 -- partial evidence exists, neither ready nor its replay can bypass that proof.
 IF phases<>0 AND (phases<>2 OR matched<>2) THEN
  RAISE EXCEPTION 'nfo ready checkpoint incomplete or conflicting' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION guard_nfo_commit_attempt() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE live_until timestamptz; scope record; active boolean; payload record; plan record; latest integer; selected record; rows_count bigint; total_bytes bigint; matched bigint; conflict boolean; extra_phase boolean:=false;
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
   EXECUTE format('SELECT version,parent_identity,target_identity FROM %I.nfo_write_commit_file_plans WHERE token=$1',TG_TABLE_SCHEMA)
   INTO plan USING NEW.token;
   -- Only the checkpoint table has a phase; a create keeps a single phase.
   IF TG_TABLE_NAME='nfo_write_commit_attempt_checkpoints' THEN extra_phase:=NEW.phase<>1; END IF;
   IF plan.version IS NULL OR get_byte(plan.parent_identity,1)<>get_byte(NEW.output_identity,1)
    OR plan.target_identity=NEW.output_identity OR plan.target_identity=NEW.rollback_identity
    OR plan.version=1 AND TG_TABLE_NAME='nfo_write_commit_attempt_ready' AND NEW.rollback_identity IS NULL
    OR plan.version=2 AND (NEW.rollback_identity IS NOT NULL OR extra_phase) THEN
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
    -- A create completes at its output checkpoint, so its ready references phase 1.
    IF TG_OP='INSERT' AND NEW.rollback_identity IS NULL THEN NEW.complete_phase:=1; END IF;
    EXECUTE format('SELECT count(*) FROM %I.nfo_write_commit_attempt_checkpoints WHERE token=$1 AND attempt=$2 AND output_identity=$3 AND (phase=1 OR rollback_identity=$4)',TG_TABLE_SCHEMA)
    INTO matched USING NEW.token,NEW.attempt,NEW.output_identity,NEW.rollback_identity;
    IF matched<>(CASE WHEN NEW.rollback_identity IS NULL THEN 1 ELSE 2 END) THEN RAISE EXCEPTION 'nfo attempt ready incomplete or conflicting' USING ERRCODE='23514'; END IF;
   END IF;
  END IF;
 END IF;
 EXECUTE format('UPDATE %I.jobs SET generation=generation WHERE id=$1',TG_TABLE_SCHEMA) USING scope.id;
 IF TG_OP='INSERT' THEN NEW.recorded_at:=clock_timestamp();NEW.lease_until:=live_until; END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION verify_nfo_commit_attempt() RETURNS trigger
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
  IF matched<>(CASE WHEN NEW.rollback_identity IS NULL THEN 1 ELSE 2 END) THEN RAISE EXCEPTION 'nfo attempt ready incomplete or conflicting' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NULL;
END $$;

-- A create token claims its media and the absent target name when its journal
-- opens; its output claim follows with the ready evidence (below).
CREATE OR REPLACE FUNCTION record_nfo_native_claims() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE entry record;
BEGIN
 EXECUTE format('SELECT e.create_target,e.native_receipt,e.relative_path FROM %I.nfo_write_entries e WHERE e.job_id=$1 AND e.sequence=$2',TG_TABLE_SCHEMA)
 INTO entry USING NEW.job_id,NEW.sequence;
 IF entry.create_target THEN
  EXECUTE format('INSERT INTO %I.nfo_write_native_claims(kind,identity,token) VALUES(''media'',substring($2 FROM 57 FOR 48),$1),(''nfo_absent'',%I.nfo_absent_target_key(substring($2 FROM octet_length($2)-47 FOR 48),regexp_replace($3,''^.*/'','''')),$1)',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
  USING NEW.token,entry.native_receipt,entry.relative_path;
  RETURN NEW;
 END IF;
 EXECUTE format('INSERT INTO %I.nfo_write_native_claims(kind,identity,token) SELECT ''media'',substring(e.native_receipt FROM 57 FOR 48),$1 FROM %I.nfo_write_entries e WHERE e.job_id=$2 AND e.sequence=$3 UNION ALL SELECT ''nfo'',substring(e.native_receipt FROM 105 FOR 48),$1 FROM %I.nfo_write_entries e WHERE e.job_id=$2 AND e.sequence=$3',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 USING NEW.token,NEW.job_id,NEW.sequence;
 RETURN NEW;
END $$;

-- Once the output is ready it becomes the target: claim its identity so a
-- rewrite of the created file cannot start while this token is unresolved.
CREATE FUNCTION claim_nfo_create_output() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE creating boolean;
BEGIN
 EXECUTE format('SELECT e.create_target FROM %I.nfo_write_commit_journal j JOIN %I.nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE j.token=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO creating USING NEW.token;
 IF creating THEN
  EXECUTE format('INSERT INTO %I.nfo_write_native_claims(kind,identity,token) VALUES(''nfo'',$1,$2)',TG_TABLE_SCHEMA)
  USING NEW.output_identity,NEW.token;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER claim_nfo_create_output_files AFTER INSERT ON nfo_write_commit_files_ready
 FOR EACH ROW EXECUTE FUNCTION claim_nfo_create_output();
CREATE TRIGGER claim_nfo_create_output_attempt AFTER INSERT ON nfo_write_commit_attempt_ready
 FOR EACH ROW EXECUTE FUNCTION claim_nfo_create_output();

CREATE OR REPLACE FUNCTION guard_nfo_native_claim() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE retained bytea; valid boolean; expected bytea; resolved boolean; entry record;
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
 EXECUTE format('SELECT e.native_receipt,e.create_target,e.relative_path FROM %I.nfo_write_commit_journal j JOIN %I.nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE j.token=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO entry USING NEW.token;
 retained:=entry.native_receipt;
 EXECUTE format('SELECT %I.nfo_write_native_receipt_valid($1)',TG_TABLE_SCHEMA) INTO valid USING retained;
 IF valid IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo native claim first observation missing' USING ERRCODE='23514';
 END IF;
 IF NEW.kind='media' THEN expected:=substring(retained FROM 57 FOR 48);
 ELSIF NEW.kind='nfo' AND NOT entry.create_target THEN expected:=substring(retained FROM 105 FOR 48);
 ELSIF NEW.kind='nfo_absent' AND entry.create_target THEN
  EXECUTE format('SELECT %I.nfo_absent_target_key(substring($1 FROM octet_length($1)-47 FOR 48),regexp_replace($2,''^.*/'',''''))',TG_TABLE_SCHEMA)
  INTO expected USING retained,entry.relative_path;
 ELSIF NEW.kind='nfo' AND entry.create_target THEN
  -- The output named by this token's own ready evidence, never user input.
  EXECUTE format('SELECT COALESCE((SELECT output_identity FROM %I.nfo_write_commit_files_ready WHERE token=$1),(SELECT output_identity FROM %I.nfo_write_commit_attempt_ready WHERE token=$1))',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
  INTO expected USING NEW.token;
 ELSE RAISE EXCEPTION 'nfo native claim kind invalid' USING ERRCODE='23514'; END IF;
 IF NEW.identity IS DISTINCT FROM expected THEN
  RAISE EXCEPTION 'nfo native claim differs from first observation' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

-- A rewrite holds exactly two claims; a create holds two, plus the output once
-- its ready evidence exists.
CREATE OR REPLACE FUNCTION verify_nfo_native_claims() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE observed bigint; expected bigint; creating boolean; ready boolean;
BEGIN
 EXECUTE format('SELECT count(*) FROM %I.nfo_write_native_claims WHERE token=$1',TG_TABLE_SCHEMA)
 INTO observed USING NEW.token;
 EXECUTE format('SELECT e.create_target FROM %I.nfo_write_commit_journal j JOIN %I.nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE j.token=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO creating USING NEW.token;
 expected:=2;
 IF creating THEN
  EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_files_ready WHERE token=$1) OR EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempt_ready WHERE token=$1)',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
  INTO ready USING NEW.token;
  IF ready THEN expected:=3; END IF;
 END IF;
 IF observed<>expected THEN RAISE EXCEPTION 'nfo physical claims incomplete' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;

-- The claims of a create: its media, the absent target name and (after ready)
-- its output, none of them held by another token. Same unresolved-history rule
-- as a rewrite.
CREATE FUNCTION check_nfo_create_claim_scope(schema_name text, job uuid, seq integer) RETURNS void
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE entry record; owned_token uuid; unknown boolean; conflicting boolean; claims bigint; absent bytea; output bytea; expected bigint;
BEGIN
 EXECUTE format('SELECT native_receipt,relative_path FROM %I.nfo_write_entries WHERE job_id=$1 AND sequence=$2',schema_name)
 INTO entry USING job,seq;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_journal j JOIN %I.nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence WHERE e.native_receipt IS NULL)',schema_name,schema_name)
 INTO unknown;
 IF unknown THEN
  RAISE EXCEPTION 'nfo historical physical observation unresolved' USING ERRCODE='23505',CONSTRAINT='nfo_native_target_unresolved';
 END IF;
 EXECUTE format('SELECT j.token FROM %I.nfo_write_commit_journal j JOIN %I.jobs k ON k.id=j.job_id AND k.generation=j.generation WHERE j.job_id=$1 AND j.sequence=$2 AND (k.owner=j.owner OR (k.owner IS NULL AND k.state IN (''failed'',''cancelled'') AND EXISTS(SELECT 1 FROM %I.nfo_write_commit_recovery_leases r WHERE r.job_id=k.id)))',schema_name,schema_name,schema_name)
 INTO owned_token USING job,seq;
 EXECUTE format('SELECT %I.nfo_absent_target_key(substring($1 FROM octet_length($1)-47 FOR 48),regexp_replace($2,''^.*/'',''''))',schema_name)
 INTO absent USING entry.native_receipt,entry.relative_path;
 IF owned_token IS NOT NULL THEN
  EXECUTE format('SELECT COALESCE((SELECT output_identity FROM %I.nfo_write_commit_files_ready WHERE token=$1),(SELECT output_identity FROM %I.nfo_write_commit_attempt_ready WHERE token=$1))',schema_name,schema_name)
  INTO output USING owned_token;
 END IF;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_native_claims WHERE (identity=substring($1 FROM 57 FOR 48) OR identity=$3 OR identity=$4) AND ($2 IS NULL OR token<>$2))',schema_name)
 INTO conflicting USING entry.native_receipt,owned_token,absent,output;
 IF conflicting THEN
  RAISE EXCEPTION 'nfo physical target unresolved' USING ERRCODE='23505',CONSTRAINT='nfo_native_target_unresolved';
 END IF;
 IF owned_token IS NOT NULL THEN
  expected:=2+(CASE WHEN output IS NULL THEN 0 ELSE 1 END);
  EXECUTE format('SELECT count(*) FROM %I.nfo_write_native_claims WHERE token=$1 AND ((kind=''media'' AND identity=substring($2 FROM 57 FOR 48)) OR (kind=''nfo_absent'' AND identity=$3) OR (kind=''nfo'' AND identity=$4))',schema_name)
  INTO claims USING owned_token,entry.native_receipt,absent,output;
  IF claims<>expected THEN
   RAISE EXCEPTION 'nfo physical claims missing' USING ERRCODE='23514';
  END IF;
 END IF;
END $$;

-- Every catalog-guarded step of a rewrite runs the previous guard unchanged. A
-- create has no target to compare, so it runs the scope and root generation
-- guards, then checks its receipt (target absent), its plan (no target
-- identity) and its claims, then the write permission as before.
ALTER FUNCTION check_nfo_commit_catalog(text,uuid,integer) RENAME TO check_nfo_commit_catalog_v87;
CREATE FUNCTION check_nfo_commit_catalog(schema_name text,job uuid,seq integer) RETURNS void
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE entry record; valid boolean; differs boolean; allowed boolean;
BEGIN
 EXECUTE format('SELECT e.create_target,e.native_receipt,e.directory_path FROM %I.nfo_write_entries e WHERE e.job_id=$1 AND e.sequence=$2',schema_name)
 INTO entry USING job,seq;
 IF entry.create_target IS NOT TRUE THEN
  EXECUTE format('SELECT %I.check_nfo_commit_catalog_v87($1,$2,$3)',schema_name) USING schema_name,job,seq;
  RETURN;
 END IF;
 EXECUTE format('SELECT %I.check_nfo_commit_catalog_v52($1,$2,$3)',schema_name) USING schema_name,job,seq;
 EXECUTE format('SELECT %I.nfo_write_native_receipt_valid($1)',schema_name) INTO valid USING entry.native_receipt;
 IF valid IS DISTINCT FROM true OR get_byte(entry.native_receipt,3)<>1 OR get_byte(entry.native_receipt,2)<>(CASE WHEN entry.directory_path='' THEN 1 ELSE 2 END) THEN
  RAISE EXCEPTION 'nfo commit native receipt missing or invalid' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_file_plans p JOIN %I.nfo_write_commit_journal j ON j.token=p.token WHERE j.job_id=$1 AND j.sequence=$2 AND (p.parent_identity IS DISTINCT FROM substring($3 FROM octet_length($3)-47 FOR 48) OR p.version<>2 OR p.target_identity IS NOT NULL))',schema_name,schema_name)
 INTO differs USING job,seq,entry.native_receipt;
 IF differs THEN
  RAISE EXCEPTION 'nfo commit plan differs from first native observation' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT %I.check_nfo_create_claim_scope($1,$2,$3)',schema_name) USING schema_name,job,seq;
 EXECUTE format('SELECT l.nfo_write_enabled FROM %I.jobs j JOIN %I.libraries l ON l.id=j.library_id WHERE j.id=$1',schema_name,schema_name)
 INTO allowed USING job;
 IF allowed IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo write disabled' USING ERRCODE='23514';
 END IF;
END $$;
COMMIT;
