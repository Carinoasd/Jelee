BEGIN;
LOCK TABLE item_images IN ACCESS EXCLUSIVE MODE;
-- Schema 94 cannot represent uploaded artwork, and an upload is a manual
-- choice whose bytes exist only in the image store: refuse to drop it.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM item_images WHERE source_kind='upload') THEN
  RAISE EXCEPTION 'retained uploaded images prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE item_images DROP CONSTRAINT item_images_upload_manual;
ALTER TABLE item_images DROP CONSTRAINT item_images_reference;
ALTER TABLE item_images ADD CONSTRAINT item_images_reference CHECK(CASE source_kind
 WHEN 'local' THEN root_id IS NOT NULL AND remote_url IS NULL AND lower(relative_path) ~ '\.(jpg|jpeg|png|webp|avif|gif|bmp|tiff)$'
 WHEN 'embedded' THEN root_id IS NOT NULL AND remote_url IS NULL
 WHEN 'remote' THEN root_id IS NULL AND remote_url IS NOT NULL
 ELSE (root_id IS NULL)<>(remote_url IS NULL) AND (root_id IS NULL OR lower(relative_path) ~ '\.(jpg|jpeg|png|webp|avif|gif|bmp|tiff)$') END);
ALTER TABLE item_images DROP CONSTRAINT item_images_source_kind_check;
ALTER TABLE item_images ADD CONSTRAINT item_images_source_kind_check CHECK(source_kind IN ('local','nfo','remote','embedded'));
COMMIT;
