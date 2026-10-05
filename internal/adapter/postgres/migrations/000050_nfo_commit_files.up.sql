BEGIN;
LOCK TABLE nfo_write_commit_journal IN SHARE ROW EXCLUSIVE MODE;
CREATE FUNCTION nfo_commit_identity_valid(v bytea, kind integer) RETURNS boolean
 LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SET search_path=pg_catalog AS $$
 SELECT CASE WHEN octet_length(v)<>48 THEN false ELSE
 get_byte(v,0)=1 AND get_byte(v,1) IN(1,2) AND get_byte(v,2)=kind AND kind IN(1,2)
 AND substring(v FROM 4 FOR 5)=decode('0000000000','hex')
 AND substring(v FROM 45 FOR 4)=decode('00000000','hex')
 AND CASE WHEN get_byte(v,1)=1 THEN substring(v FROM 41 FOR 4)=decode('00000000','hex')
 ELSE substring(v FROM 25 FOR 8)=decode('0000000000000000','hex')
 AND get_byte(v,40)::bigint+get_byte(v,41)::bigint*256+get_byte(v,42)::bigint*65536+get_byte(v,43)::bigint*16777216<1000000000 END END
$$;
CREATE TABLE nfo_write_commit_file_plans (
 token uuid PRIMARY KEY REFERENCES nfo_write_commit_journal(token) ON DELETE RESTRICT,
 version smallint NOT NULL CHECK(version=1),
 target_name text NOT NULL CHECK(octet_length(target_name) BETWEEN 1 AND 1024 AND lower(target_name) LIKE '%.nfo' AND target_name !~ '[/\\:[:cntrl:]]'),
 parent_identity bytea NOT NULL CHECK(nfo_commit_identity_valid(parent_identity,2)),
 target_identity bytea NOT NULL CHECK(nfo_commit_identity_valid(target_identity,1)),
 CHECK(get_byte(parent_identity,1)=get_byte(target_identity,1)),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at)
);
CREATE TABLE nfo_write_commit_files_ready (
 token uuid PRIMARY KEY REFERENCES nfo_write_commit_file_plans(token) ON DELETE RESTRICT,
 output_identity bytea NOT NULL CHECK(nfo_commit_identity_valid(output_identity,1)),
 rollback_identity bytea NOT NULL CHECK(nfo_commit_identity_valid(rollback_identity,1)),
 CHECK(get_byte(output_identity,1)=get_byte(rollback_identity,1) AND output_identity<>rollback_identity),
 recorded_at timestamptz NOT NULL CHECK(isfinite(recorded_at)),
 lease_until timestamptz NOT NULL CHECK(isfinite(lease_until) AND lease_until>recorded_at)
);
CREATE FUNCTION guard_nfo_commit_files() RETURNS trigger
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
CREATE FUNCTION verify_nfo_commit_files_lease() RETURNS trigger
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
CREATE TRIGGER guard_nfo_commit_file_plan BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_file_plans
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_files();
CREATE TRIGGER guard_nfo_commit_files_ready BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_commit_files_ready
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_files();
CREATE CONSTRAINT TRIGGER verify_nfo_commit_file_plan AFTER INSERT OR UPDATE ON nfo_write_commit_file_plans
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_commit_files_lease();
CREATE CONSTRAINT TRIGGER verify_nfo_commit_files_ready AFTER INSERT OR UPDATE ON nfo_write_commit_files_ready
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_commit_files_lease();
COMMIT;
