BEGIN;
-- Schema 73 keeps appearance, plugin settings and layouts in each browser.
-- These settings only change presentation; the downgrade drops them, and
-- their audit records stay.
ALTER TABLE user_preferences DROP COLUMN layout;
DROP TABLE site_plugins;
DROP TABLE site_appearance;
COMMIT;
