BEGIN;
LOCK TABLE libraries,catalog_scan_items IN ACCESS EXCLUSIVE MODE;
-- The previous schema has no naming options: a library an administrator
-- configured would silently fall back to the defaults. Turn the options off
-- first. The item columns only repeat file-name facts; the previous parser
-- version plans the files again and recomputes what it understands.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM libraries WHERE naming_date_episodes OR naming_absolute_episodes) THEN
  RAISE EXCEPTION 'library naming options prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE catalog_scan_items DROP CONSTRAINT catalog_scan_items_season_range_check;
ALTER TABLE catalog_scan_items DROP COLUMN special;
ALTER TABLE catalog_scan_items DROP COLUMN air_date;
ALTER TABLE catalog_scan_items DROP COLUMN absolute;
ALTER TABLE catalog_scan_items DROP COLUMN season_end;
ALTER TABLE libraries DROP COLUMN naming_absolute_episodes;
ALTER TABLE libraries DROP COLUMN naming_date_episodes;
COMMIT;
