BEGIN;
LOCK TABLE libraries IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM libraries WHERE metadata_preferences_revision<>1 OR metadata_image_languages<>ARRAY['zh','ja','en','null']::text[]) THEN
  RAISE EXCEPTION 'retained image language preferences prevent rollback' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE libraries DROP COLUMN metadata_image_languages;
COMMIT;
