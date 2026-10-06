BEGIN;
-- Schema 84 configures log retention and levels only through the process
-- configuration. The stored overrides and retention are operational
-- settings, not data: dropping them returns every instance to its
-- configured levels and the backup count limit.
DROP TABLE log_settings;
DROP FUNCTION guard_log_settings();
COMMIT;
