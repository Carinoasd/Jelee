BEGIN;
-- Schema 72 has no stored interface preferences; the web client falls back
-- to following the system theme. Nothing else depends on them.
DROP INDEX client_rules_block_lookup_idx;
DROP TABLE user_preferences;
COMMIT;
