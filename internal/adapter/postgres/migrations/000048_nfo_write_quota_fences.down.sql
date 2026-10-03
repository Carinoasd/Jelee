BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE nfo_write_preparations,nfo_write_entries IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_preparations) OR EXISTS(SELECT 1 FROM nfo_write_entries) THEN
  RAISE EXCEPTION 'cannot remove nfo quota fences with retained data' USING ERRCODE='23514';
 END IF;
END $$;
DROP TRIGGER aa_fence_nfo_write_preparation ON nfo_write_preparations;
DROP TRIGGER aa_fence_nfo_write_entry ON nfo_write_entries;
DROP FUNCTION fence_nfo_write_quota();
DROP TABLE nfo_write_quota_fences;
COMMIT;
