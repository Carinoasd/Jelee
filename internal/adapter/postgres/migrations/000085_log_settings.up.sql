BEGIN;
-- Logging settings shared by every instance (G46.2, G46.9). One row holds
-- the retention of ordinary log files (age in days and a total size cap;
-- zero switches a limit off) and the runtime level overrides an
-- administrator set through the API or jelee-cli. Each instance reads the
-- row periodically and applies it to its own log router; audit and security
-- retention stay in audit_retention and are independent of these values.
-- level_overrides is a JSON array of {component, level, expiresAt?}: an
-- empty component is the global level, an override without expiresAt stays
-- until it is reset. The audit and security scopes never appear in it
-- (G46.10); the application refuses them before writing.
CREATE TABLE log_settings (
 singleton boolean PRIMARY KEY DEFAULT true CONSTRAINT log_settings_singleton_check CHECK (singleton),
 log_days integer NOT NULL DEFAULT 30 CONSTRAINT log_settings_log_days_check CHECK (log_days BETWEEN 0 AND 3650),
 log_max_total_mb integer NOT NULL DEFAULT 2048 CONSTRAINT log_settings_log_max_total_mb_check CHECK (log_max_total_mb BETWEEN 0 AND 1048576),
 level_overrides jsonb NOT NULL DEFAULT '[]'::jsonb CONSTRAINT log_settings_level_overrides_check CHECK (jsonb_typeof(level_overrides)='array' AND jsonb_array_length(level_overrides)<=32 AND octet_length(level_overrides::text)<=8192),
 revision bigint NOT NULL DEFAULT 1 CONSTRAINT log_settings_revision_check CHECK (revision>0),
 updated_at timestamptz NOT NULL DEFAULT now() CONSTRAINT log_settings_updated_at_check CHECK (isfinite(updated_at))
);
INSERT INTO log_settings DEFAULT VALUES;
CREATE FUNCTION guard_log_settings() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
BEGIN
 RAISE EXCEPTION 'log settings are retained' USING ERRCODE='23514';
END $$;
CREATE TRIGGER guard_log_settings BEFORE DELETE ON log_settings FOR EACH ROW EXECUTE FUNCTION guard_log_settings();
CREATE TRIGGER guard_log_settings_truncate BEFORE TRUNCATE ON log_settings FOR EACH STATEMENT EXECUTE FUNCTION guard_log_settings();
COMMIT;
