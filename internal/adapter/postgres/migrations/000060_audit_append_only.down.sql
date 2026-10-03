BEGIN;
LOCK TABLE audit_logs,audit_retention IN ACCESS EXCLUSIVE MODE;
-- The previous schema requires a UUID target and has no request id or retention
-- settings, so rows or settings that it cannot represent prevent downgrade.
-- The category column is dropped: it is derived from the event name.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM audit_logs WHERE target_id IS NULL OR request_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM audit_retention WHERE audit_days<>365 OR security_days<>365) THEN
  RAISE EXCEPTION 'audit targets, request ids or retention settings are retained' USING ERRCODE='23514';
 END IF;
END $$;
DROP FUNCTION purge_audit_logs(text,integer);
DROP TRIGGER guard_audit_logs_truncate ON audit_logs;
DROP TRIGGER guard_audit_logs_change ON audit_logs;
DROP TRIGGER guard_audit_logs_insert ON audit_logs;
DROP FUNCTION guard_audit_logs();
DROP TABLE audit_retention;
DROP FUNCTION guard_audit_retention();
DROP INDEX audit_logs_target_idx;
DROP INDEX audit_logs_retention_idx;
ALTER TABLE audit_logs
 DROP CONSTRAINT audit_logs_state_size,
 DROP CONSTRAINT audit_logs_event_format,
 DROP CONSTRAINT audit_logs_single_target,
 DROP COLUMN target_ref,
 DROP COLUMN request_id,
 DROP COLUMN category,
 ALTER COLUMN target_id SET NOT NULL;
COMMIT;
