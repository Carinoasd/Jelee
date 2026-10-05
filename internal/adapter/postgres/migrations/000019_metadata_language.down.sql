BEGIN;
LOCK TABLE libraries IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM libraries WHERE metadata_language <> 'zh-CN' OR metadata_preferences_revision <> 1) THEN
  RAISE EXCEPTION 'retained metadata preferences prevent rollback' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE libraries DROP COLUMN metadata_preferences_revision, DROP COLUMN metadata_language;
COMMIT;
