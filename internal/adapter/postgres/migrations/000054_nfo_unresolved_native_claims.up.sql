BEGIN;
LOCK TABLE jobs,nfo_write_entries,nfo_write_commit_journal,nfo_write_commit_file_plans,nfo_write_commit_files_ready IN ACCESS EXCLUSIVE MODE;

CREATE TABLE nfo_write_native_claims (
 kind text NOT NULL CHECK(kind IN ('nfo','media')),
 identity bytea NOT NULL CHECK(octet_length(identity)=48),
 token uuid NOT NULL REFERENCES nfo_write_commit_journal(token) ON DELETE RESTRICT,
 CONSTRAINT nfo_native_target_unresolved PRIMARY KEY(identity),
 UNIQUE(token,kind)
);

CREATE FUNCTION guard_nfo_native_claim() RETURNS trigger
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
CREATE TRIGGER guard_nfo_native_claim BEFORE INSERT OR UPDATE OR DELETE ON nfo_write_native_claims
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_native_claim();

-- Copy only already-retained first observations. Historical NULL stays unknown.
-- A conflicting retained token makes this transaction fail; no winner is chosen.
INSERT INTO nfo_write_native_claims(kind,identity,token)
 SELECT 'media',substring(e.native_receipt FROM 57 FOR 48),j.token
 FROM nfo_write_commit_journal j JOIN nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence
 WHERE e.native_receipt IS NOT NULL
 UNION ALL
 SELECT 'nfo',substring(e.native_receipt FROM 105 FOR 48),j.token
 FROM nfo_write_commit_journal j JOIN nfo_write_entries e ON e.job_id=j.job_id AND e.sequence=j.sequence
 WHERE e.native_receipt IS NOT NULL;

CREATE FUNCTION record_nfo_native_claims() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
BEGIN
 EXECUTE format('INSERT INTO %I.nfo_write_native_claims(kind,identity,token) SELECT ''media'',substring(e.native_receipt FROM 57 FOR 48),$1 FROM %I.nfo_write_entries e WHERE e.job_id=$2 AND e.sequence=$3 UNION ALL SELECT ''nfo'',substring(e.native_receipt FROM 105 FOR 48),$1 FROM %I.nfo_write_entries e WHERE e.job_id=$2 AND e.sequence=$3',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
 USING NEW.token,NEW.job_id,NEW.sequence;
 RETURN NEW;
END $$;
CREATE TRIGGER record_nfo_native_claims AFTER INSERT ON nfo_write_commit_journal
 FOR EACH ROW EXECUTE FUNCTION record_nfo_native_claims();

CREATE FUNCTION check_nfo_native_claim_scope(schema_name text,job uuid,seq integer) RETURNS void
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

ALTER FUNCTION check_nfo_commit_catalog(text,uuid,integer) RENAME TO check_nfo_commit_catalog_v53;
CREATE FUNCTION check_nfo_commit_catalog(schema_name text,job uuid,seq integer) RETURNS void
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
BEGIN
 EXECUTE format('SELECT %I.check_nfo_commit_catalog_v53($1,$2,$3)',schema_name) USING schema_name,job,seq;
 EXECUTE format('SELECT %I.check_nfo_native_claim_scope($1,$2,$3)',schema_name) USING schema_name,job,seq;
END $$;

CREATE FUNCTION verify_nfo_native_claims() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE observed bigint;
BEGIN
 EXECUTE format('SELECT count(*) FROM %I.nfo_write_native_claims WHERE token=$1',TG_TABLE_SCHEMA)
 INTO observed USING NEW.token;
 IF observed<>2 THEN RAISE EXCEPTION 'nfo physical claims incomplete' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER verify_nfo_native_claims AFTER INSERT OR UPDATE ON nfo_write_native_claims
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_native_claims();
COMMIT;
