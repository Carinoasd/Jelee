BEGIN;
LOCK TABLE jobs,nfo_write_entries,nfo_write_commit_journal,nfo_write_native_claims IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_native_claims) OR EXISTS(SELECT 1 FROM nfo_write_commit_journal) THEN
  RAISE EXCEPTION 'nfo unresolved physical claims or journal are retained' USING ERRCODE='23514';
 END IF;
END $$;
DROP FUNCTION check_nfo_commit_catalog(text,uuid,integer);
ALTER FUNCTION check_nfo_commit_catalog_v53(text,uuid,integer) RENAME TO check_nfo_commit_catalog;
DROP FUNCTION check_nfo_native_claim_scope(text,uuid,integer);
DROP TRIGGER record_nfo_native_claims ON nfo_write_commit_journal;
DROP FUNCTION record_nfo_native_claims();
DROP TRIGGER verify_nfo_native_claims ON nfo_write_native_claims;
DROP FUNCTION verify_nfo_native_claims();
DROP TRIGGER guard_nfo_native_claim ON nfo_write_native_claims;
DROP FUNCTION guard_nfo_native_claim();
DROP TABLE nfo_write_native_claims;
COMMIT;
