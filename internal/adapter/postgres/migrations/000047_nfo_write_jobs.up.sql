BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write'));
ALTER TABLE jobs DROP CONSTRAINT jobs_error_code_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_error_code_check CHECK(error_code IN ('','scan_unavailable','scan_io','scan_limit','scan_failed','job_timeout','job_attempts_exhausted','catalog_import_failed','nfo_write_failed'));
ALTER TABLE jobs ADD CONSTRAINT jobs_nfo_write_error_check CHECK(kind='nfo_write' OR error_code<>'nfo_write_failed');

CREATE TABLE nfo_write_requests (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 library_id uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 total integer NOT NULL CHECK(total BETWEEN 1 AND 100),
 intent_digest bytea NOT NULL CHECK(octet_length(intent_digest)=32),
 UNIQUE(job_id,library_id,generation)
);
-- Reuse the exact payload bounds/hashes of preparation storage, but own the
-- bytes for the job lifetime. Preparation identity is a snapshot, not an FK.
CREATE TABLE nfo_write_entries (LIKE nfo_write_preparations INCLUDING CONSTRAINTS);
ALTER TABLE nfo_write_entries
 DROP COLUMN id,
 DROP COLUMN actor_id,
 DROP COLUMN idempotency_key,
 DROP COLUMN created_at,
 DROP COLUMN expires_at,
 ADD COLUMN job_id uuid NOT NULL,
 ADD COLUMN sequence integer NOT NULL CHECK(sequence BETWEEN 1 AND 100),
 ADD COLUMN preparation_id uuid NOT NULL,
 ADD PRIMARY KEY(job_id,sequence),
 ADD UNIQUE(job_id,item_id),
 ADD FOREIGN KEY(job_id,library_id,generation) REFERENCES nfo_write_requests(job_id,library_id,generation) ON DELETE CASCADE;

CREATE FUNCTION guard_nfo_write_intent() RETURNS trigger
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
CREATE TRIGGER guard_nfo_write_request BEFORE INSERT OR UPDATE ON nfo_write_requests
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_intent();
CREATE TRIGGER guard_nfo_write_entry BEFORE INSERT OR UPDATE ON nfo_write_entries
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_intent();

CREATE FUNCTION verify_nfo_write_job() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE target uuid; job_kind text; job_library uuid; request_library uuid; expected integer; actual bigint; first_sequence integer; last_sequence integer;
BEGIN
 IF TG_TABLE_NAME='jobs' THEN target:=NEW.id;
 ELSIF TG_OP='DELETE' THEN target:=OLD.job_id;
 ELSE target:=NEW.job_id;
 END IF;
 EXECUTE format('SELECT kind,library_id FROM %I.jobs WHERE id=$1',TG_TABLE_SCHEMA)
 INTO job_kind,job_library USING target;
 IF job_kind IS NULL THEN RETURN NULL; END IF;
 EXECUTE format('SELECT library_id,total FROM %I.nfo_write_requests WHERE job_id=$1',TG_TABLE_SCHEMA)
 INTO request_library,expected USING target;
 IF job_kind<>'nfo_write' THEN
  IF expected IS NOT NULL THEN
   RAISE EXCEPTION 'nfo intent attached to a different job kind' USING ERRCODE='23514';
  END IF;
  RETURN NULL;
 END IF;
 IF expected IS NULL OR request_library<>job_library THEN
  RAISE EXCEPTION 'nfo write request is missing or unbound' USING ERRCODE='23514';
 END IF;
 EXECUTE format('SELECT count(*),min(sequence),max(sequence) FROM %I.nfo_write_entries WHERE job_id=$1',TG_TABLE_SCHEMA)
 INTO actual,first_sequence,last_sequence USING target;
 IF actual<>expected OR first_sequence<>1 OR last_sequence<>expected THEN
  RAISE EXCEPTION 'nfo write batch is incomplete' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER verify_nfo_write_job AFTER INSERT OR UPDATE ON jobs
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_write_job();
CREATE CONSTRAINT TRIGGER verify_nfo_write_request AFTER INSERT OR UPDATE OR DELETE ON nfo_write_requests
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_write_job();
CREATE CONSTRAINT TRIGGER verify_nfo_write_entry AFTER INSERT OR UPDATE OR DELETE ON nfo_write_entries
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION verify_nfo_write_job();

ALTER TABLE job_metric_totals DROP CONSTRAINT job_metric_totals_kind_check;
ALTER TABLE job_metric_totals ADD CONSTRAINT job_metric_totals_kind_check CHECK(kind IN ('inventory_scan','catalog_import','nfo_write'));
INSERT INTO job_metric_totals(kind,priority) VALUES('nfo_write','background'),('nfo_write','manual');
INSERT INTO job_metric_buckets(kind,priority,measure,bucket_index,upper_bound_microseconds)
 SELECT 'nfo_write',priority,measure,bucket_index,upper_bound_microseconds
 FROM job_metric_buckets WHERE kind='inventory_scan';
COMMIT;
