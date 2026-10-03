BEGIN;
LOCK TABLE nfo_write_preparations,nfo_write_entries,nfo_write_commit_journal IN ACCESS EXCLUSIVE MODE;
-- Bounded canonical shape only; database bytes do not certify physical identity.
CREATE FUNCTION nfo_write_native_receipt_valid(v bytea) RETURNS boolean
 LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE SET search_path=pg_catalog AS $$
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
-- No backfill: historical NULL is observable but cannot authorize execution.
ALTER TABLE nfo_write_preparations ADD COLUMN native_receipt bytea
 CHECK(native_receipt IS NULL OR nfo_write_native_receipt_valid(native_receipt));
ALTER TABLE nfo_write_entries ADD COLUMN native_receipt bytea
 CHECK(native_receipt IS NULL OR nfo_write_native_receipt_valid(native_receipt));
CREATE FUNCTION guard_nfo_write_native_receipt() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE valid boolean; source_row record;
BEGIN
 IF TG_OP='UPDATE' AND OLD.native_receipt IS NULL AND NEW.native_receipt IS NULL THEN RETURN NEW; END IF;
 EXECUTE format('SELECT %I.nfo_write_native_receipt_valid($1)',TG_TABLE_SCHEMA) INTO valid USING NEW.native_receipt;
 IF valid IS DISTINCT FROM true OR NEW.root_generation IS NULL THEN
  RAISE EXCEPTION 'nfo write native receipt missing or invalid' USING ERRCODE='23514';
 END IF;
 IF get_byte(NEW.native_receipt,2)<>(CASE WHEN NEW.directory_path='' THEN 1 ELSE 2 END) THEN
  RAISE EXCEPTION 'nfo write native media kind differs' USING ERRCODE='23514';
 END IF;
 IF TG_TABLE_NAME='nfo_write_entries' AND TG_OP='INSERT' AND TG_WHEN='BEFORE' THEN
  EXECUTE format('SELECT p.native_receipt,p.version,p.request_digest,p.library_id,p.item_id,p.source_id,p.root_id,p.kind,p.revision,p.generation,p.root_generation,p.root_path,p.relative_path,p.media_path,p.directory_path,p.max_bytes,p.modified_unix_nano,p.original_sha256,p.replacement_sha256 FROM %I.nfo_write_preparations p JOIN %I.jobs j ON j.id=$1 AND j.actor_id=p.actor_id AND j.library_id=p.library_id WHERE p.id=$2 AND p.expires_at>clock_timestamp()',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
  INTO source_row USING NEW.job_id,NEW.preparation_id;
  IF source_row.native_receipt IS NULL OR source_row.native_receipt IS DISTINCT FROM NEW.native_receipt THEN
   RAISE EXCEPTION 'nfo write native receipt differs from preparation' USING ERRCODE='23514';
  END IF;
  IF ROW(source_row.version,source_row.request_digest,source_row.library_id,source_row.item_id,source_row.source_id,source_row.root_id,source_row.kind,source_row.revision,source_row.generation,source_row.root_generation,source_row.root_path,source_row.relative_path,source_row.media_path,source_row.directory_path,source_row.max_bytes,source_row.modified_unix_nano,source_row.original_sha256,source_row.replacement_sha256)
  IS DISTINCT FROM ROW(NEW.version,NEW.request_digest,NEW.library_id,NEW.item_id,NEW.source_id,NEW.root_id,NEW.kind,NEW.revision,NEW.generation,NEW.root_generation,NEW.root_path,NEW.relative_path,NEW.media_path,NEW.directory_path,NEW.max_bytes,NEW.modified_unix_nano,NEW.original_sha256,NEW.replacement_sha256) THEN
   RAISE EXCEPTION 'nfo write intent differs from first prepared scope' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_nfo_write_preparation_native BEFORE INSERT OR UPDATE ON nfo_write_preparations
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_native_receipt();
CREATE TRIGGER guard_nfo_write_entry_native BEFORE INSERT OR UPDATE ON nfo_write_entries
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_native_receipt();
CREATE CONSTRAINT TRIGGER verify_nfo_write_preparation_native AFTER INSERT OR UPDATE ON nfo_write_preparations
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_native_receipt();
CREATE CONSTRAINT TRIGGER verify_nfo_write_entry_native AFTER INSERT OR UPDATE ON nfo_write_entries
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_native_receipt();
ALTER FUNCTION check_nfo_commit_catalog(text,uuid,integer) RENAME TO check_nfo_commit_catalog_v52;
CREATE FUNCTION check_nfo_commit_catalog(schema_name text,job uuid,seq integer) RETURNS void
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE retained bytea; directory text; valid boolean; differs boolean;
BEGIN
 EXECUTE format('SELECT %I.check_nfo_commit_catalog_v52($1,$2,$3)',schema_name) USING schema_name,job,seq;
 EXECUTE format('SELECT native_receipt,directory_path FROM %I.nfo_write_entries WHERE job_id=$1 AND sequence=$2',schema_name)
 INTO retained,directory USING job,seq;
 EXECUTE format('SELECT %I.nfo_write_native_receipt_valid($1)',schema_name) INTO valid USING retained;
 IF valid IS DISTINCT FROM true OR get_byte(retained,2)<>(CASE WHEN directory='' THEN 1 ELSE 2 END) THEN
  RAISE EXCEPTION 'nfo commit native receipt missing or invalid' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.nfo_write_commit_file_plans p JOIN %I.nfo_write_commit_journal j ON j.token=p.token WHERE j.job_id=$1 AND j.sequence=$2 AND (p.parent_identity IS DISTINCT FROM substring($3 FROM octet_length($3)-47 FOR 48) OR p.target_identity IS DISTINCT FROM substring($3 FROM 105 FOR 48)))',schema_name,schema_name)
 INTO differs USING job,seq,retained;
 IF differs THEN
  RAISE EXCEPTION 'nfo commit plan differs from first native observation' USING ERRCODE='23514';
 END IF;
END $$;
-- Compare NEW because the catalog BEFORE trigger cannot see an incoming plan.
-- Repeat at deferred commit and no-op replay. The last ancestor is the NFO
-- parent; the NFO record came from the handle supplying original bytes.
CREATE FUNCTION guard_nfo_commit_plan_native() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
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
CREATE TRIGGER guard_nfo_commit_plan_native BEFORE INSERT OR UPDATE ON nfo_write_commit_file_plans
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_plan_native();
CREATE CONSTRAINT TRIGGER verify_nfo_commit_plan_native AFTER INSERT OR UPDATE ON nfo_write_commit_file_plans
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_nfo_commit_plan_native();
CREATE OR REPLACE FUNCTION guard_nfo_write_preparation() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE total_rows bigint; actor_rows bigint; library_bytes bigint; total_bytes bigint; incoming bigint;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW IS DISTINCT FROM OLD THEN
   RAISE EXCEPTION 'nfo write preparation is immutable' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 PERFORM pg_advisory_xact_lock(hashtext(TG_TABLE_SCHEMA),17481247);
 EXECUTE format('SELECT count(*),count(*) FILTER(WHERE actor_id=$1),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+COALESCE(octet_length(native_receipt),0)),0),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+COALESCE(octet_length(native_receipt),0)) FILTER(WHERE library_id=$2),0) FROM %I.nfo_write_preparations',TG_TABLE_SCHEMA)
 INTO total_rows,actor_rows,total_bytes,library_bytes USING NEW.actor_id,NEW.library_id;
 incoming:=octet_length(NEW.request_bytes)::bigint+octet_length(NEW.original_bytes)+octet_length(NEW.replacement_bytes)+COALESCE(octet_length(NEW.native_receipt),0);
 IF total_rows>=256 OR actor_rows>=32 OR total_bytes+incoming>268435456 OR library_bytes+incoming>134217728 THEN
  RAISE EXCEPTION 'nfo write preparation capacity reached' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION guard_nfo_write_intent() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE rows_count bigint; total_bytes bigint; job_bytes bigint; incoming bigint;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW IS DISTINCT FROM OLD THEN
   RAISE EXCEPTION 'nfo write intent is immutable' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 IF TG_TABLE_NAME='nfo_write_entries' THEN
  PERFORM pg_advisory_xact_lock(hashtext(TG_TABLE_SCHEMA),17481248);
  EXECUTE format('SELECT count(*),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+COALESCE(octet_length(native_receipt),0)),0),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)+COALESCE(octet_length(native_receipt),0)) FILTER(WHERE job_id=$1),0) FROM %I.nfo_write_entries',TG_TABLE_SCHEMA)
  INTO rows_count,total_bytes,job_bytes USING NEW.job_id;
  incoming:=octet_length(NEW.request_bytes)::bigint+octet_length(NEW.original_bytes)+octet_length(NEW.replacement_bytes)+COALESCE(octet_length(NEW.native_receipt),0);
  IF rows_count>=1024 OR total_bytes+incoming>536870912 OR job_bytes+incoming>134217728 THEN
   RAISE EXCEPTION 'nfo write intent capacity reached' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
COMMIT;
