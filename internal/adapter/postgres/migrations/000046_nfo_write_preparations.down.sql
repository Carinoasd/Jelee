BEGIN;
LOCK TABLE nfo_write_preparations IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_preparations) THEN
  RAISE EXCEPTION 'retain nfo write preparations before downgrade';
 END IF;
END $$;
DROP TABLE nfo_write_preparations;
DROP FUNCTION guard_nfo_write_preparation();
COMMIT;
