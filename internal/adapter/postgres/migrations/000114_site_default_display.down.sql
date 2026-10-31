BEGIN;
-- Schema 113 has nowhere to keep the site default display choices. They only
-- change how the web client presents itself, so the downgrade drops them like
-- the users' own display choices.
ALTER TABLE site_appearance DROP COLUMN default_display;
COMMIT;
