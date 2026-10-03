BEGIN;
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE nfo_write_commit_journal IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_commit_journal) THEN
  RAISE EXCEPTION 'cannot remove unresolved nfo commit journal' USING ERRCODE='23514';
 END IF;
END $$;
DROP TRIGGER retain_nfo_write_commit_job ON jobs;
DROP FUNCTION retain_nfo_write_commit_job();
DROP TRIGGER verify_nfo_write_commit_lease ON nfo_write_commit_journal;
DROP FUNCTION verify_nfo_write_commit_lease();
DROP TRIGGER guard_nfo_write_commit_journal ON nfo_write_commit_journal;
DROP FUNCTION guard_nfo_write_commit_journal();
DROP TABLE nfo_write_commit_journal;
COMMIT;
