BEGIN;
LOCK TABLE tool_versions IN ACCESS EXCLUSIVE MODE;
-- Windows amd64 runs the pinned ffprobe.exe under a restricted token and Job
-- Object (ADR 0011), so its probe identity may now be recorded. The platform
-- is part of the identity digest; Linux rows and their cache entries are
-- untouched. Only ffprobe has a Windows identity.
ALTER TABLE tool_versions DROP CONSTRAINT tool_versions_platform_check;
ALTER TABLE tool_versions ADD CONSTRAINT tool_versions_platform_check CHECK (platform IN ('linux-amd64','windows-amd64'));
COMMIT;
