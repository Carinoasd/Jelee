BEGIN;
LOCK TABLE jobs,nfo_write_entries,nfo_write_commit_journal,nfo_write_commit_file_plans,nfo_write_commit_files_ready,nfo_write_commit_attempt_ready,nfo_write_commit_recovery_leases,nfo_write_native_claims IN ACCESS EXCLUSIVE MODE;
-- Settlement progress of one token, append-only. Phase 1 (backed up) is saved
-- before the target Rename; 2 (replaced) and 3 (rolled back) are terminal. A
-- token without phase 1 was never renamed onto its target by this protocol.
CREATE TABLE nfo_write_commit_settlements (
 token uuid NOT NULL REFERENCES nfo_write_commit_journal(token) ON DELETE RESTRICT,
 phase smallint NOT NULL CHECK(phase BETWEEN 1 AND 3),
 attempt smallint NOT NULL CHECK(attempt BETWEEN 0 AND 3),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at),
 PRIMARY KEY(token,phase)
);
-- A resolved job has no token between backup and a terminal phase. Nothing can
-- be added to its evidence afterwards; epoch 0 names the original job lease.
CREATE TABLE nfo_write_commit_resolutions (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE RESTRICT,
 owner text NOT NULL CHECK(octet_length(owner) BETWEEN 1 AND 128),
 epoch bigint NOT NULL CHECK(epoch>=0),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at))
);

CREATE OR REPLACE FUNCTION nfo_commit_live_lease(schema_name text,job uuid,journal_owner text,journal_generation bigint) RETURNS timestamptz
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE current record; recovery timestamptz; resolved boolean;
BEGIN
 EXECUTE format('SELECT kind,state,owner,generation,lease_until,cancel_requested FROM %I.jobs WHERE id=$1',schema_name)
 INTO current USING job;
 IF current.kind IS DISTINCT FROM 'nfo_write' OR current.generation IS DISTINCT FROM journal_generation THEN
  RETURN NULL;
 END IF;
 -- A resolution closes every lease of the job, original or recovery.
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_resolutions WHERE job_id=$1)',schema_name)
 INTO resolved USING job;
 IF resolved THEN
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

CREATE FUNCTION guard_nfo_commit_settlement() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE scope record; live_until timestamptz; live_actor boolean; ready boolean; other record;
BEGIN
 IF TG_OP='DELETE' OR (TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'nfo commit settlement is immutable' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT j.id,j.state,j.cancel_requested,j.actor_id,w.owner AS journal_owner,w.generation AS journal_generation FROM %I.nfo_write_commit_journal w JOIN %I.jobs j ON j.id=w.job_id WHERE w.token=$1 FOR UPDATE OF j',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO scope USING NEW.token;
 IF scope.id IS NOT NULL THEN
  EXECUTE format('SELECT %I.nfo_commit_live_lease($1,$2,$3,$4)',TG_TABLE_SCHEMA) INTO live_until
  USING TG_TABLE_SCHEMA,scope.id,scope.journal_owner,scope.journal_generation;
 END IF;
 IF scope.id IS NULL OR live_until IS NULL THEN
  RAISE EXCEPTION 'nfo commit settlement lease is not live' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT is_admin AND NOT disabled AND deleted_at IS NULL FROM %I.users WHERE id=$1 FOR SHARE',TG_TABLE_SCHEMA)
 INTO live_actor USING scope.actor_id;
 IF live_actor IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit settlement actor is not active' USING ERRCODE='23514';
 END IF;
 IF TG_OP='INSERT' THEN
  -- Only a completely prepared namespace can be settled.
  IF NEW.attempt=0 THEN
   EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_files_ready WHERE token=$1) AND NOT EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempt_ready WHERE token=$1)',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
   INTO ready USING NEW.token;
  ELSE
   EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_attempt_ready WHERE token=$1 AND attempt=$2)',TG_TABLE_SCHEMA)
   INTO ready USING NEW.token,NEW.attempt;
  END IF;
  IF ready IS DISTINCT FROM true THEN
   RAISE EXCEPTION 'nfo commit settlement requires ready evidence' USING ERRCODE='23514';
  END IF;
  EXECUTE format('SELECT bool_or(phase=1) AS backed_up,bool_or(phase=2) AS replaced,bool_or(phase=3) AS rolled_back,bool_or(attempt<>$2) AS mismatched FROM %I.nfo_write_commit_settlements WHERE token=$1',TG_TABLE_SCHEMA)
  INTO other USING NEW.token,NEW.attempt;
  IF other.mismatched THEN
   RAISE EXCEPTION 'nfo commit settlement changed its attempt' USING ERRCODE='23514';
  END IF;
  -- A stopped, cancelled job is only rolled back: it can neither start a
  -- backup that permits a Rename nor confirm a replacement.
  IF NEW.phase<>3 AND scope.state<>'running' AND scope.cancel_requested THEN
   RAISE EXCEPTION 'nfo commit recovery cannot replace for a cancelled job' USING ERRCODE='23514';
  END IF;
  IF NEW.phase=1 AND (other.replaced OR other.rolled_back) THEN
   RAISE EXCEPTION 'nfo commit settlement is already terminal' USING ERRCODE='23514';
  ELSIF NEW.phase=2 AND (other.backed_up IS DISTINCT FROM true OR other.rolled_back) THEN
   RAISE EXCEPTION 'nfo commit replacement requires a backup phase' USING ERRCODE='23514';
  ELSIF NEW.phase=3 AND other.replaced THEN
   RAISE EXCEPTION 'nfo commit settlement is already terminal' USING ERRCODE='23514';
  END IF;
 END IF;
 EXECUTE format('UPDATE %I.jobs SET generation=generation WHERE id=$1',TG_TABLE_SCHEMA) USING scope.id;
 IF TG_OP='INSERT' THEN
  NEW.recorded_at:=clock_timestamp(); NEW.lease_until:=live_until;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_nfo_write_commit_settlements BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_settlements
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_settlement();
-- A rollback only restores the prepared original, so it stays possible after
-- catalog drift; a backup or replacement needs the current catalog scope.
CREATE TRIGGER catalog_nfo_write_commit_settlements BEFORE INSERT OR UPDATE ON nfo_write_commit_settlements
 FOR EACH ROW WHEN (NEW.phase<>3) EXECUTE FUNCTION guard_nfo_commit_catalog();
CREATE CONSTRAINT TRIGGER lease_nfo_write_commit_settlements AFTER INSERT OR UPDATE ON nfo_write_commit_settlements
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_commit_files_lease();
CREATE CONSTRAINT TRIGGER verify_catalog_nfo_write_commit_settlements AFTER INSERT OR UPDATE ON nfo_write_commit_settlements
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.phase<>3) EXECUTE FUNCTION guard_nfo_commit_catalog();

CREATE FUNCTION guard_nfo_commit_resolution() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE job record; live boolean; pending boolean;
BEGIN
 IF TG_OP='DELETE' OR (TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'nfo commit resolution is immutable' USING ERRCODE='23514';
 END IF;
 IF TG_OP='UPDATE' THEN RETURN NEW; END IF;
 EXECUTE format('SELECT kind,state,owner,lease_until FROM %I.jobs WHERE id=$1 FOR UPDATE',TG_TABLE_SCHEMA)
 INTO job USING NEW.job_id;
 IF job.kind IS DISTINCT FROM 'nfo_write' THEN
  RAISE EXCEPTION 'nfo commit resolution requires an nfo write job' USING ERRCODE='23514';
 END IF;
 -- The holder of the original or the recovery lease resolves. Resolution adds
 -- no filesystem authority, so a disabled actor cannot strand the job.
 IF NEW.epoch=0 THEN
  live:=job.state='running' AND job.owner=NEW.owner AND job.lease_until>clock_timestamp();
 ELSE
  EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_recovery_leases WHERE job_id=$1 AND owner=$2 AND epoch=$3 AND lease_until>clock_timestamp() FOR SHARE)',TG_TABLE_SCHEMA)
  INTO live USING NEW.job_id,NEW.owner,NEW.epoch;
  live:=live AND job.state IN ('failed','cancelled') AND job.owner IS NULL;
 END IF;
 IF live IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit resolution lease is not live' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_journal w JOIN %I.nfo_write_commit_settlements s ON s.token=w.token AND s.phase=1 WHERE w.job_id=$1 AND NOT EXISTS(SELECT 1 FROM %I.nfo_write_commit_settlements t WHERE t.token=w.token AND t.phase>1))',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO pending USING NEW.job_id;
 IF pending THEN
  RAISE EXCEPTION 'nfo commit settlement is unresolved' USING ERRCODE='23514';
 END IF;
 EXECUTE format('UPDATE %I.jobs SET generation=generation WHERE id=$1',TG_TABLE_SCHEMA) USING NEW.job_id;
 NEW.recorded_at:=clock_timestamp();
 RETURN NEW;
END $$;
CREATE TRIGGER guard_nfo_write_commit_resolutions BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_resolutions
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_resolution();
-- Physical claims protect targets only while a token may still be renamed.
CREATE FUNCTION release_nfo_native_claims() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
BEGIN
 EXECUTE format('DELETE FROM %I.nfo_write_native_claims c USING %I.nfo_write_commit_journal w WHERE c.token=w.token AND w.job_id=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 USING NEW.job_id;
 RETURN NEW;
END $$;
CREATE TRIGGER release_nfo_native_claims AFTER INSERT ON nfo_write_commit_resolutions
 FOR EACH ROW EXECUTE FUNCTION release_nfo_native_claims();

CREATE OR REPLACE FUNCTION guard_nfo_native_claim() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
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

-- Journaled jobs still stop freely. Success needs a resolution and a replaced
-- settlement for every entry; a stopped job reaches it only after recovery.
CREATE OR REPLACE FUNCTION retain_nfo_write_commit_job() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE pending boolean; complete boolean;
BEGIN
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_journal WHERE job_id=$1)',TG_TABLE_SCHEMA)
 INTO pending USING OLD.id;
 IF NOT pending THEN
  IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
 END IF;
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'nfo commit recovery data is retained' USING ERRCODE='23514';
 END IF;
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.kind IS DISTINCT FROM OLD.kind
  OR NEW.library_id IS DISTINCT FROM OLD.library_id OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
  OR NEW.generation IS DISTINCT FROM OLD.generation THEN
  RAISE EXCEPTION 'nfo commit requires recovery before job transition' USING ERRCODE='23514';
 END IF;
 IF NEW.state IS DISTINCT FROM OLD.state THEN
  IF OLD.state='running' AND NEW.state IN ('failed','cancelled') AND NEW.owner IS NULL AND NEW.lease_until IS NULL THEN
   RETURN NEW;
  END IF;
  IF NEW.state='succeeded' AND NEW.owner IS NULL AND NEW.lease_until IS NULL AND (OLD.state='running' OR (OLD.state='failed' AND NOT NEW.cancel_requested)) THEN
   EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_resolutions WHERE job_id=$1) AND NOT EXISTS(SELECT 1 FROM %I.nfo_write_entries e WHERE e.job_id=$1 AND NOT EXISTS(SELECT 1 FROM %I.nfo_write_commit_journal w JOIN %I.nfo_write_commit_settlements s ON s.token=w.token AND s.phase=2 WHERE w.job_id=e.job_id AND w.sequence=e.sequence))',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
   INTO complete USING OLD.id;
   IF complete THEN RETURN NEW; END IF;
  END IF;
  RAISE EXCEPTION 'nfo commit requires recovery before job transition' USING ERRCODE='23514';
 ELSIF NEW.owner IS DISTINCT FROM OLD.owner THEN
  RAISE EXCEPTION 'nfo commit requires recovery before job transition' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
COMMIT;
