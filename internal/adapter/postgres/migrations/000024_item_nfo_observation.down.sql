BEGIN;
LOCK TABLE item_nfo_observations IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM item_nfo_observations) THEN
  RAISE EXCEPTION 'retained item NFO observations prevent rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE item_nfo_observations;
COMMIT;
