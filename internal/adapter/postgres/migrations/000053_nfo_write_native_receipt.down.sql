BEGIN;
LOCK TABLE nfo_write_preparations,nfo_write_entries,nfo_write_commit_journal IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_preparations WHERE native_receipt IS NOT NULL)
 OR EXISTS(SELECT 1 FROM nfo_write_entries WHERE native_receipt IS NOT NULL)
 OR EXISTS(SELECT 1 FROM nfo_write_commit_journal) THEN
  RAISE EXCEPTION 'nfo native observations or journal evidence are retained' USING ERRCODE='23514';
 END IF;
END $$;
DROP TRIGGER verify_nfo_commit_plan_native ON nfo_write_commit_file_plans;
DROP TRIGGER guard_nfo_commit_plan_native ON nfo_write_commit_file_plans;
DROP FUNCTION guard_nfo_commit_plan_native();
DROP FUNCTION check_nfo_commit_catalog(text,uuid,integer);
ALTER FUNCTION check_nfo_commit_catalog_v52(text,uuid,integer) RENAME TO check_nfo_commit_catalog;
DROP TRIGGER verify_nfo_write_entry_native ON nfo_write_entries;
DROP TRIGGER verify_nfo_write_preparation_native ON nfo_write_preparations;
DROP TRIGGER guard_nfo_write_entry_native ON nfo_write_entries;
DROP TRIGGER guard_nfo_write_preparation_native ON nfo_write_preparations;
DROP FUNCTION guard_nfo_write_native_receipt();
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
 EXECUTE format('SELECT count(*),count(*) FILTER(WHERE actor_id=$1),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),0),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)) FILTER(WHERE library_id=$2),0) FROM %I.nfo_write_preparations',TG_TABLE_SCHEMA)
 INTO total_rows,actor_rows,total_bytes,library_bytes USING NEW.actor_id,NEW.library_id;
 incoming:=octet_length(NEW.request_bytes)::bigint+octet_length(NEW.original_bytes)+octet_length(NEW.replacement_bytes);
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
  EXECUTE format('SELECT count(*),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),0),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)) FILTER(WHERE job_id=$1),0) FROM %I.nfo_write_entries',TG_TABLE_SCHEMA)
  INTO rows_count,total_bytes,job_bytes USING NEW.job_id;
  incoming:=octet_length(NEW.request_bytes)::bigint+octet_length(NEW.original_bytes)+octet_length(NEW.replacement_bytes);
  IF rows_count>=1024 OR total_bytes+incoming>536870912 OR job_bytes+incoming>134217728 THEN
   RAISE EXCEPTION 'nfo write intent capacity reached' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
ALTER TABLE nfo_write_entries DROP COLUMN native_receipt;
ALTER TABLE nfo_write_preparations DROP COLUMN native_receipt;
DROP FUNCTION nfo_write_native_receipt_valid(bytea);
COMMIT;
