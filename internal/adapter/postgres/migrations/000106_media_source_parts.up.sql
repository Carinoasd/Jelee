BEGIN;
-- G21.1: multi-part versions. Files of one directory whose names differ only
-- in a part marker (movie.cd1.mkv, movie.cd2.mkv) and whose part numbers run
-- 1..n form one version of their item. Every part stays a media source of
-- its own, delivered as it is; the first part is the version. The rows are
-- derived by catalog synchronisation from file names and rebuilt there.
CREATE TABLE media_source_parts(
 source_id uuid PRIMARY KEY REFERENCES media_sources(id) ON DELETE CASCADE,
 -- Removing the first part dissolves the version: the remaining files are
 -- versions of their own again until the next synchronisation decides.
 version_source_id uuid NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
 part_number integer NOT NULL CHECK(part_number BETWEEN 1 AND 99),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(version_source_id,part_number),
 CHECK((part_number=1)=(source_id=version_source_id))
);

-- Why a file with a part marker is or is not part of a multi-part version
-- (merged, single, gap, duplicate, mixed, manual); NULL without a marker.
ALTER TABLE catalog_scan_sources ADD COLUMN part_reason text CHECK(part_reason ~ '^[a-z_]{1,64}$');
-- The parts step pages over the items of a library.
CREATE INDEX catalog_scan_sources_library_item_idx ON catalog_scan_sources(library_id,item_id);

-- The versions phase first assembles parts, page by page, then groups
-- versions across directories.
ALTER TABLE catalog_sync_requests ADD COLUMN parts_cursor uuid;
ALTER TABLE catalog_sync_requests ADD COLUMN parts_done boolean NOT NULL DEFAULT false;
COMMIT;
