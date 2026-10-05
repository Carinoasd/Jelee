BEGIN;
LOCK TABLE audit_logs IN ACCESS EXCLUSIVE MODE;
-- G07.7: a permanently deleted user's audit events are kept for their
-- retention period but de-identified. The event, its time, category, request
-- id and the actor/target UUIDs stay (the UUID no longer resolves to any
-- account); the actor IP of the user's own requests becomes NULL and the
-- before/after states about the user or the user's sessions, application
-- passwords and shares become a fixed marker.
CREATE INDEX audit_logs_actor_idx ON audit_logs(actor_id,id) WHERE actor_id IS NOT NULL;

-- Same guard as schema 60 plus one narrow UPDATE path, open only while
-- redact_audit_subject() runs (its function-scoped jelee.audit_redact
-- setting): every column but actor_ip and the states is unchanged, actor_ip
-- may only become NULL for an actor that is no longer an account, and a
-- state may only become the marker for a row whose actor or target is no
-- longer an account.
CREATE OR REPLACE FUNCTION guard_audit_logs() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE days integer; marker jsonb:='{"redacted":"user_purged"}'; actor_gone boolean; target_gone boolean;
BEGIN
 IF TG_LEVEL='ROW' AND TG_OP='INSERT' THEN
  NEW.occurred_at:=now();
  RETURN NEW;
 END IF;
 IF TG_LEVEL='ROW' AND TG_OP='UPDATE' AND current_setting('jelee.audit_redact',true)='on'
  AND NEW.id=OLD.id AND NEW.event=OLD.event AND NEW.category=OLD.category AND NEW.occurred_at=OLD.occurred_at
  AND NEW.request_id IS NOT DISTINCT FROM OLD.request_id AND NEW.target_ref IS NOT DISTINCT FROM OLD.target_ref
  AND NEW.target_id IS NOT DISTINCT FROM OLD.target_id AND NEW.actor_id IS NOT DISTINCT FROM OLD.actor_id THEN
  EXECUTE format('SELECT $1 IS NULL OR NOT EXISTS(SELECT 1 FROM %I.users WHERE id=$1),$2 IS NULL OR NOT EXISTS(SELECT 1 FROM %I.users WHERE id=$2)',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA)
  INTO actor_gone,target_gone USING OLD.actor_id,OLD.target_id;
  IF (NEW.actor_ip IS NOT DISTINCT FROM OLD.actor_ip OR NEW.actor_ip IS NULL AND actor_gone)
   AND (NEW.before_state IS NOT DISTINCT FROM OLD.before_state OR NEW.before_state=marker AND (actor_gone OR target_gone))
   AND (NEW.after_state IS NOT DISTINCT FROM OLD.after_state OR NEW.after_state=marker AND (actor_gone OR target_gone)) THEN
   RETURN NEW;
  END IF;
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

-- The single redaction path. subject is the deleted user, objects the ids of
-- the sessions, application passwords and shares that were theirs. It runs
-- after the user row is gone and returns the number of rows changed.
CREATE FUNCTION redact_audit_subject(target_schema text,subject uuid,objects uuid[]) RETURNS bigint
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog SET jelee.audit_redact='on' AS $$
DECLARE changed bigint;
BEGIN
 IF subject IS NULL OR objects IS NULL THEN
  RAISE EXCEPTION 'audit redaction needs a subject' USING ERRCODE='22023';
 END IF;
 EXECUTE format('UPDATE %I.audit_logs a SET
  actor_ip=CASE WHEN a.actor_id=$1 OR a.actor_id IS NULL AND a.target_id=$1 THEN NULL ELSE a.actor_ip END,
  before_state=CASE WHEN a.before_state IS NOT NULL AND jsonb_typeof(a.before_state)<>''null'' AND (a.target_id=$1 OR a.target_id=ANY($2)) THEN ''{"redacted":"user_purged"}''::jsonb ELSE a.before_state END,
  after_state=CASE WHEN a.after_state IS NOT NULL AND jsonb_typeof(a.after_state)<>''null'' AND (a.target_id=$1 OR a.target_id=ANY($2)) THEN ''{"redacted":"user_purged"}''::jsonb ELSE a.after_state END
  WHERE (a.actor_id=$1 AND a.actor_ip IS NOT NULL) OR (a.actor_id IS NULL AND a.target_id=$1 AND a.actor_ip IS NOT NULL)
   OR ((a.target_id=$1 OR a.target_id=ANY($2)) AND (jsonb_typeof(a.before_state) IS DISTINCT FROM ''null'' AND a.before_state IS DISTINCT FROM ''{"redacted":"user_purged"}''::jsonb AND a.before_state IS NOT NULL
    OR jsonb_typeof(a.after_state) IS DISTINCT FROM ''null'' AND a.after_state IS DISTINCT FROM ''{"redacted":"user_purged"}''::jsonb AND a.after_state IS NOT NULL))',target_schema)
 USING subject,objects;
 GET DIAGNOSTICS changed=ROW_COUNT;
 RETURN changed;
END $$;
COMMIT;
