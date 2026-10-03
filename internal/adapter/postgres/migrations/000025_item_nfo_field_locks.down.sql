BEGIN;
LOCK TABLE item_nfo_field_locks IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM item_nfo_field_locks) THEN
  RAISE EXCEPTION 'retained item NFO field locks prevent rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE item_nfo_field_locks;
COMMIT;
