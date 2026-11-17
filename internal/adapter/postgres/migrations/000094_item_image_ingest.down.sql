BEGIN;
-- The older schema cannot tell manual rows apart; rows the scan wrote stay
-- as ordinary unlocked references.
DROP INDEX library_inventory_image_directory_idx;
DROP FUNCTION inventory_parent_directory(text);
ALTER TABLE item_images DROP COLUMN manual;
COMMIT;
