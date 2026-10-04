BEGIN;
-- Schema 71 has no developer mode: every relaxation simply ends, which is
-- the production default, so nothing blocks the downgrade. The audit trail
-- of past sessions stays in audit_logs.
DROP TABLE dev_mode_tokens;
DROP TABLE dev_mode_state;
COMMIT;
