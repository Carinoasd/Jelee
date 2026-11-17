BEGIN;
-- The dominant color is derived from stored bytes and is recomputed when
-- they are read again; dropping it loses nothing that cannot be rebuilt.
ALTER TABLE item_images DROP CONSTRAINT item_images_dominant_color_content;
ALTER TABLE item_images DROP COLUMN dominant_color;
COMMIT;
