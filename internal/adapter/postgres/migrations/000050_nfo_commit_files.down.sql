BEGIN;
LOCK TABLE nfo_write_commit_file_plans,nfo_write_commit_files_ready IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_commit_file_plans) OR EXISTS(SELECT 1 FROM nfo_write_commit_files_ready) THEN
  RAISE EXCEPTION 'nfo commit file recovery data is retained' USING ERRCODE='23514';
 END IF;
END $$;
DROP TABLE nfo_write_commit_files_ready;
DROP TABLE nfo_write_commit_file_plans;
DROP FUNCTION verify_nfo_commit_files_lease();
DROP FUNCTION guard_nfo_commit_files();
DROP FUNCTION nfo_commit_identity_valid(bytea,integer);
COMMIT;
