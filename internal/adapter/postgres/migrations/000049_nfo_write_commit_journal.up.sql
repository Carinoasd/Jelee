BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE nfo_write_entries IN SHARE ROW EXCLUSIVE MODE;
-- This is the unresolved, immutable first record. It does not certify a
-- filesystem commit. Resolution requires a later native recovery protocol.
CREATE TABLE nfo_write_commit_journal (
 job_id uuid NOT NULL,
 sequence integer NOT NULL CHECK(sequence BETWEEN 1 AND 100),
 generation bigint NOT NULL CHECK(generation>0),
 token uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
 owner text NOT NULL CHECK(octet_length(owner) BETWEEN 1 AND 128),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at),
 PRIMARY KEY(job_id,sequence,generation),
 FOREIGN KEY(job_id,sequence) REFERENCES nfo_write_entries(job_id,sequence) ON DELETE RESTRICT
);
CREATE FUNCTION guard_nfo_write_commit_journal() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE current_job record; live_actor boolean; records bigint;
BEGIN
 IF TG_OP<>'INSERT' THEN
  IF TG_OP='UPDATE' AND NEW IS NOT DISTINCT FROM OLD THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'nfo commit journal is immutable' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT kind,state,owner,generation,lease_until,cancel_requested,actor_id,attempts,max_attempts FROM %I.jobs WHERE id=$1 FOR UPDATE',TG_TABLE_SCHEMA)
 INTO current_job USING NEW.job_id;
 IF current_job.kind IS DISTINCT FROM 'nfo_write' OR current_job.state IS DISTINCT FROM 'running'
  OR current_job.owner IS DISTINCT FROM NEW.owner OR current_job.generation IS DISTINCT FROM NEW.generation
  OR current_job.cancel_requested OR current_job.lease_until IS NULL OR current_job.lease_until<=clock_timestamp()
  OR current_job.attempts<1 OR current_job.attempts>current_job.max_attempts THEN
  RAISE EXCEPTION 'nfo commit journal lease is not live' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT is_admin AND NOT disabled AND deleted_at IS NULL FROM %I.users WHERE id=$1 FOR SHARE',TG_TABLE_SCHEMA)
 INTO live_actor USING current_job.actor_id;
 IF live_actor IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit journal actor is not active' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT count(*) FROM %I.nfo_write_commit_journal WHERE job_id=$1 AND sequence=$2',TG_TABLE_SCHEMA)
 INTO records USING NEW.job_id,NEW.sequence;
 IF records>=10 THEN
  RAISE EXCEPTION 'nfo commit journal capacity reached' USING ERRCODE='23514';
 END IF;
 -- A row lock alone does not invalidate a repeatable-read snapshot. Touch the
 -- job version so stale job mutations cannot miss this new journal record.
 EXECUTE format('UPDATE %I.jobs SET generation=generation WHERE id=$1',TG_TABLE_SCHEMA)
 USING NEW.job_id;
 NEW.recorded_at:=clock_timestamp();
 NEW.lease_until:=current_job.lease_until;
 IF NEW.lease_until<=NEW.recorded_at THEN
  RAISE EXCEPTION 'nfo commit journal lease is not live' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_nfo_write_commit_journal BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_journal
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_commit_journal();

CREATE FUNCTION verify_nfo_write_commit_lease() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE live boolean;
BEGIN
 EXECUTE format('SELECT j.kind=''nfo_write'' AND j.state=''running'' AND j.owner=$2 AND j.generation=$3 AND j.lease_until>clock_timestamp() AND NOT j.cancel_requested AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL FROM %I.jobs j JOIN %I.users u ON u.id=j.actor_id WHERE j.id=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 INTO live USING NEW.job_id,NEW.owner,NEW.generation;
 IF live IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'nfo commit journal lease is not live' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER verify_nfo_write_commit_lease AFTER INSERT ON nfo_write_commit_journal
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_write_commit_lease();

CREATE FUNCTION retain_nfo_write_commit_job() RETURNS trigger
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
CREATE TRIGGER retain_nfo_write_commit_job BEFORE UPDATE OR DELETE ON jobs
 FOR EACH ROW EXECUTE FUNCTION retain_nfo_write_commit_job();
COMMIT;
