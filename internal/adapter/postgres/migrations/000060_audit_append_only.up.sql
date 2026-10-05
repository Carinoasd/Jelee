BEGIN;
LOCK TABLE audit_logs IN ACCESS EXCLUSIVE MODE;
-- Audit rows gain a fixed category (audit or security), an optional request
-- correlation id and an optional non-UUID target reference. target_id stays a
-- nullable uuid so every existing UUID query keeps working; target_ref holds
-- targets that are not UUIDs (settings names and similar). A row carries at
-- most one of the two.
ALTER TABLE audit_logs
 ADD COLUMN category text NOT NULL DEFAULT 'audit' CHECK (category IN ('audit','security')),
 ADD COLUMN request_id text CHECK (request_id IS NULL OR request_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
 ADD COLUMN target_ref text CHECK (target_ref IS NULL OR (octet_length(target_ref) BETWEEN 1 AND 256 AND target_ref !~ '[[:cntrl:]]')),
 ALTER COLUMN target_id DROP NOT NULL,
 ADD CONSTRAINT audit_logs_single_target CHECK (target_id IS NULL OR target_ref IS NULL);
-- Historical rows are retained exactly as written; these bounds apply to new rows.
ALTER TABLE audit_logs
 ADD CONSTRAINT audit_logs_event_format CHECK (octet_length(event)<=64 AND event ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+$') NOT VALID,
 ADD CONSTRAINT audit_logs_state_size CHECK (octet_length(before_state::text)<=65536 AND octet_length(after_state::text)<=65536) NOT VALID;
UPDATE audit_logs SET category='security' WHERE event='login.failed';
CREATE INDEX audit_logs_retention_idx ON audit_logs(category,occurred_at);
CREATE INDEX audit_logs_target_idx ON audit_logs(target_id,id) WHERE target_id IS NOT NULL;

-- Audit and security retention are independent of ordinary log retention.
CREATE TABLE audit_retention (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 audit_days integer NOT NULL DEFAULT 365 CHECK (audit_days BETWEEN 7 AND 36500),
 security_days integer NOT NULL DEFAULT 365 CHECK (security_days BETWEEN 7 AND 36500),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
 updated_at timestamptz NOT NULL DEFAULT now() CHECK (isfinite(updated_at))
);
INSERT INTO audit_retention DEFAULT VALUES;
CREATE FUNCTION guard_audit_retention() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
BEGIN
 RAISE EXCEPTION 'audit retention settings are retained' USING ERRCODE='23514';
END $$;
CREATE TRIGGER guard_audit_retention BEFORE DELETE ON audit_retention FOR EACH ROW EXECUTE FUNCTION guard_audit_retention();
CREATE TRIGGER guard_audit_retention_truncate BEFORE TRUNCATE ON audit_retention FOR EACH STATEMENT EXECUTE FUNCTION guard_audit_retention();

-- Audit rows are append-only. INSERT stamps the database clock so callers
-- cannot backdate an entry into an expired retention window. UPDATE and
-- TRUNCATE are always rejected. DELETE is accepted only while
-- purge_audit_logs() runs (its function-scoped jelee.audit_purge setting) and
-- only for a row older than the retention of its own category, so even a
-- session that forges the setting cannot remove a row inside retention.
CREATE FUNCTION guard_audit_logs() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE days integer;
BEGIN
 IF TG_LEVEL='ROW' AND TG_OP='INSERT' THEN
  NEW.occurred_at:=now();
  RETURN NEW;
 END IF;
 IF TG_LEVEL='ROW' AND TG_OP='DELETE' AND current_setting('jelee.audit_purge',true)='on' THEN
  EXECUTE format('SELECT CASE $1 WHEN ''security'' THEN security_days ELSE audit_days END FROM %I.audit_retention WHERE singleton',TG_TABLE_SCHEMA)
  INTO days USING OLD.category;
  IF days IS NOT NULL AND OLD.occurred_at<now()-make_interval(days=>days) THEN
   RETURN OLD;
  END IF;
 END IF;
 RAISE EXCEPTION 'audit log is append-only' USING ERRCODE='23514';
END $$;
CREATE TRIGGER guard_audit_logs_insert BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION guard_audit_logs();
CREATE TRIGGER guard_audit_logs_change BEFORE UPDATE OR DELETE ON audit_logs FOR EACH ROW EXECUTE FUNCTION guard_audit_logs();
CREATE TRIGGER guard_audit_logs_truncate BEFORE TRUNCATE ON audit_logs FOR EACH STATEMENT EXECUTE FUNCTION guard_audit_logs();

-- The single controlled deletion path. It removes at most batch expired rows
-- (oldest first) and reports the count per category.
CREATE FUNCTION purge_audit_logs(target_schema text,batch integer) RETURNS TABLE(purged_category text,purged bigint)
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog SET jelee.audit_purge='on' AS $$
DECLARE retention record;
BEGIN
 IF batch IS NULL OR batch<1 OR batch>10000 THEN
  RAISE EXCEPTION 'audit purge batch is out of range' USING ERRCODE='22023';
 END IF;
 EXECUTE format('SELECT audit_days,security_days FROM %I.audit_retention WHERE singleton FOR SHARE',target_schema) INTO retention;
 IF retention IS NULL THEN
  RAISE EXCEPTION 'audit retention is not configured' USING ERRCODE='23514';
 END IF;
 RETURN QUERY EXECUTE format('WITH doomed AS (SELECT id FROM %I.audit_logs WHERE (category=''audit'' AND occurred_at<now()-make_interval(days=>$1)) OR (category=''security'' AND occurred_at<now()-make_interval(days=>$2)) ORDER BY id LIMIT $3 FOR UPDATE), gone AS (DELETE FROM %I.audit_logs a USING doomed d WHERE a.id=d.id RETURNING a.category) SELECT c.name,(SELECT count(*) FROM gone g WHERE g.category=c.name) FROM (VALUES (''audit''),(''security'')) c(name) ORDER BY c.name',target_schema,target_schema)
 USING retention.audit_days,retention.security_days,batch;
END $$;
COMMIT;
