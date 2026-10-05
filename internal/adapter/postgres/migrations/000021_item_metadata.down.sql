BEGIN;
LOCK TABLE item_metadata_state,item_metadata_fields IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM item_metadata_state) OR EXISTS(SELECT 1 FROM item_metadata_fields) THEN
  RAISE EXCEPTION 'retained item metadata prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE item_metadata_fields;
DROP TABLE item_metadata_state;
COMMIT;
