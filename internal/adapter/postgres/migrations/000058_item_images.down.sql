BEGIN;
LOCK TABLE item_images,image_variants IN ACCESS EXCLUSIVE MODE;
-- Image references carry user locks and manual choices that schema 57 cannot
-- represent. The variant index is a rebuildable cache of the store directory
-- and is dropped with the schema.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM item_images) THEN
  RAISE EXCEPTION 'retained item images prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE image_variants;
DROP TABLE item_images;
COMMIT;
