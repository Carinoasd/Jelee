BEGIN;
LOCK TABLE audit_logs IN ACCESS EXCLUSIVE MODE;
-- Redacted rows stay redacted; the schema 60 guard rejects every update.
DROP FUNCTION redact_audit_subject(text,uuid,uuid[]);
CREATE OR REPLACE FUNCTION guard_audit_logs() RETURNS trigger
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
DROP INDEX audit_logs_actor_idx;
COMMIT;
