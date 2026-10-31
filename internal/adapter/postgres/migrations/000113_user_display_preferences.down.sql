BEGIN;
-- Schema 112 has nowhere to keep display choices. They only change how the
-- web client presents itself, so the downgrade drops them like the page layout.
ALTER TABLE user_preferences DROP COLUMN display;
COMMIT;
