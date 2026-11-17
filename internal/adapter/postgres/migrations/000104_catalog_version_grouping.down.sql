BEGIN;
LOCK TABLE catalog_sync_requests,catalog_scan_items,item_version_operations IN ACCESS EXCLUSIVE MODE;
-- Automatic merges stay ordinary merges the previous schema can undo; the
-- previous schema never groups automatically, so nothing else depends on the
-- flag. A synchronisation inside the versions phase has finished every
-- catalog change it must make and ends as done.
UPDATE catalog_sync_requests SET phase='done' WHERE phase='versions';
DROP INDEX item_metadata_facts_nfo_unique_ids_idx;
DROP INDEX catalog_scan_items_versions_idx;
ALTER TABLE item_version_operations DROP CONSTRAINT item_version_operations_automatic_check;
ALTER TABLE item_version_operations DROP COLUMN automatic;
ALTER TABLE catalog_scan_items DROP COLUMN grouping_reason;
ALTER TABLE catalog_scan_items DROP COLUMN grouping_evidence;
ALTER TABLE catalog_sync_requests DROP COLUMN aggregated;
ALTER TABLE catalog_sync_requests DROP COLUMN versions_cursor;
ALTER TABLE catalog_sync_requests DROP CONSTRAINT catalog_sync_requests_phase_check;
ALTER TABLE catalog_sync_requests ADD CONSTRAINT catalog_sync_requests_phase_check CHECK(phase IN ('publish','sources','missing','pending','done'));
COMMIT;
