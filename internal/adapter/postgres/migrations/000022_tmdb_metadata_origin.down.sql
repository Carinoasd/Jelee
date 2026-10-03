BEGIN;
LOCK TABLE item_metadata_fields IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM item_metadata_fields WHERE source='tmdb' OR provider_resource IS NOT NULL OR provider_id IS NOT NULL OR provider_source_url IS NOT NULL OR provider_language IS NOT NULL OR provider_fetched_at IS NOT NULL) THEN
  RAISE EXCEPTION 'retained provider metadata prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_provider_origin_check;
ALTER TABLE item_metadata_fields DROP COLUMN provider_resource,DROP COLUMN provider_id,DROP COLUMN provider_source_url,DROP COLUMN provider_language,DROP COLUMN provider_fetched_at;
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_source_check;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_fields_source_check CHECK(source IN ('existing','manual'));
COMMIT;
