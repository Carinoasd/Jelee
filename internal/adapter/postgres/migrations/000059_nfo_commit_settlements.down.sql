BEGIN;
LOCK TABLE jobs,nfo_write_commit_journal,nfo_write_commit_settlements,nfo_write_commit_resolutions,nfo_write_native_claims IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_commit_settlements) OR EXISTS(SELECT 1 FROM nfo_write_commit_resolutions) OR EXISTS(SELECT 1 FROM nfo_write_commit_journal) THEN
  RAISE EXCEPTION 'nfo commit settlements, resolutions or journal are retained' USING ERRCODE='23514';
 END IF;
END $$;
DROP TRIGGER release_nfo_native_claims ON nfo_write_commit_resolutions;
DROP FUNCTION release_nfo_native_claims();
DROP TRIGGER guard_nfo_write_commit_resolutions ON nfo_write_commit_resolutions;
DROP FUNCTION guard_nfo_commit_resolution();
DROP TRIGGER verify_catalog_nfo_write_commit_settlements ON nfo_write_commit_settlements;
DROP TRIGGER lease_nfo_write_commit_settlements ON nfo_write_commit_settlements;
DROP TRIGGER catalog_nfo_write_commit_settlements ON nfo_write_commit_settlements;
DROP TRIGGER guard_nfo_write_commit_settlements ON nfo_write_commit_settlements;
DROP FUNCTION guard_nfo_commit_settlement();
DROP TABLE nfo_write_commit_resolutions;
DROP TABLE nfo_write_commit_settlements;
CREATE OR REPLACE FUNCTION nfo_commit_live_lease(schema_name text,job uuid,journal_owner text,journal_generation bigint) RETURNS timestamptz
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
CREATE OR REPLACE FUNCTION guard_nfo_native_claim() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE retained bytea; valid boolean; expected bytea;
BEGIN
 IF TG_OP='DELETE' OR (TG_OP='UPDATE' AND NEW IS DISTINCT FROM OLD) THEN
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
CREATE OR REPLACE FUNCTION retain_nfo_write_commit_job() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE pending boolean;
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
 -- Stopping is allowed; reporting success, requeueing or assigning a new owner
 -- must wait for recovery. Cancellation remains observable and cannot erase WAL.
 IF NEW.state IS DISTINCT FROM OLD.state THEN
  IF OLD.state<>'running' OR NEW.state NOT IN ('failed','cancelled') OR NEW.owner IS NOT NULL OR NEW.lease_until IS NOT NULL THEN
   RAISE EXCEPTION 'nfo commit requires recovery before job transition' USING ERRCODE='23514';
  END IF;
 ELSIF NEW.owner IS DISTINCT FROM OLD.owner THEN
  RAISE EXCEPTION 'nfo commit requires recovery before job transition' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
COMMIT;
