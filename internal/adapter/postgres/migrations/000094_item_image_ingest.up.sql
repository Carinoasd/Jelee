BEGIN;
-- G40.2/G40.3 scan ingestion of item images.
--
-- manual marks a row a person wrote through the image API. The catalog sync
-- never replaces or removes such a row, locked or not; it only maintains the
-- rows it wrote itself. Rows written before this migration came from a
-- person or a metadata import, so they are kept out of scan maintenance.
-- Embedded covers are written by the probe pass and stay non-manual.
ALTER TABLE item_images ADD COLUMN manual boolean NOT NULL DEFAULT false;
UPDATE item_images SET manual=true WHERE source_kind<>'embedded';

-- The parent folder of a scanned file ('' for the root folder), so a catalog
-- sync page lists the image files of its item folders with one indexed
-- lookup per folder. Only image rows pay for this index.
CREATE FUNCTION inventory_parent_directory(file_path text) RETURNS text
 LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
 SELECT regexp_replace(file_path,'(^|/)[^/]+$','')
$$;
CREATE INDEX library_inventory_image_directory_idx ON library_inventory_baseline_data(library_id,snapshot_id,root_id,inventory_parent_directory(path))
 WHERE kind='image';
COMMIT;
