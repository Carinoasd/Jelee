BEGIN;
LOCK TABLE tool_versions IN ACCESS EXCLUSIVE MODE;
-- A Windows identity row is referenced by cached results, running phases or
-- pending requests of that platform. The previous schema cannot represent
-- it, so any such row blocks the downgrade; removing them (clear the probe
-- cache, or let the maintenance sweep drop unreferenced identities) is an
-- explicit operator decision.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM tool_versions WHERE platform<>'linux-amd64') THEN
  RAISE EXCEPTION 'non-Linux probe identities prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE tool_versions DROP CONSTRAINT tool_versions_platform_check;
ALTER TABLE tool_versions ADD CONSTRAINT tool_versions_platform_check CHECK (platform='linux-amd64');
COMMIT;
