BEGIN;
LOCK TABLE item_version_exclusions,catalog_scan_item_aliases IN ACCESS EXCLUSIVE MODE;
-- Schema 77 synchronisation knows neither exclusions nor aliases: it would
-- silently group a split file into its old item again and recreate merged
-- items. Lift the exclusions and undo the merges first.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM item_version_exclusions) OR EXISTS(SELECT 1 FROM catalog_scan_item_aliases) THEN
  RAISE EXCEPTION 'retained manual version decisions prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
-- Track preferences and main versions only change which version and tracks
-- a client starts with; the operation log only serves undo. They go.
DROP TABLE user_track_preferences;
DROP TABLE item_primary_versions;
ALTER TABLE catalog_scan_sources DROP COLUMN manual;
DROP TABLE catalog_scan_item_aliases;
DROP TABLE item_version_exclusions;
DROP TABLE item_version_operations;
COMMIT;
