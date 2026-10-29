BEGIN;
LOCK TABLE catalog_sync_requests,catalog_scan_sources,media_source_parts IN ACCESS EXCLUSIVE MODE;
-- Parts are derived from file names and hold no decision of their own; the
-- previous schema lists every part as a version of its item again.
DROP TABLE media_source_parts;
DROP INDEX catalog_scan_sources_library_item_idx;
ALTER TABLE catalog_scan_sources DROP COLUMN part_reason;
ALTER TABLE catalog_sync_requests DROP COLUMN parts_done;
ALTER TABLE catalog_sync_requests DROP COLUMN parts_cursor;
COMMIT;
