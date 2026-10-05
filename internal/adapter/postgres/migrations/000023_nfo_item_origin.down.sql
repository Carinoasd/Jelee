BEGIN;
LOCK TABLE item_metadata_fields IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM item_metadata_fields WHERE source='nfo' OR nfo_origin IS NOT NULL) THEN
  RAISE EXCEPTION 'retained NFO metadata prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_nfo_origin_check;
ALTER TABLE item_metadata_fields DROP COLUMN nfo_origin;
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_source_check;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_fields_source_check CHECK(source IN ('existing','manual','tmdb'));
COMMIT;
