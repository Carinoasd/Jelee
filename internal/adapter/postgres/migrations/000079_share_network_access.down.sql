BEGIN;
LOCK TABLE users, share_links, library_network_rules, client_rules, client_control_hits IN ACCESS EXCLUSIVE MODE;
-- The previous schema has no network restrictions, no request-level library
-- restriction and no share links. Dropping a network rule or a
-- restrict_libraries rule would silently show what it hides, and dropping
-- a live share would turn its guest sessions into plain accounts, so they
-- block the downgrade; removing them (or revoking the shares) first is an
-- explicit operator decision. Revoked and expired shares, their guest
-- accounts and sessions are dropped; their audit records stay.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM library_network_rules)
  OR EXISTS(SELECT 1 FROM client_rules WHERE COALESCE(intent,action)='restrict_libraries')
  OR EXISTS(SELECT 1 FROM share_links WHERE revoked_at IS NULL AND expires_at>now()) THEN
  RAISE EXCEPTION 'network rules, restrict_libraries rules or live share links prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DELETE FROM client_control_hits WHERE action='restrict_libraries';
ALTER TABLE client_control_hits
 DROP CONSTRAINT client_control_hits_action_check,
 ADD CONSTRAINT client_control_hits_action_check CHECK(action IN ('allow','deny','read_only','rate_limit','force_relogin','pending_approval'));
ALTER TABLE client_rules
 DROP CONSTRAINT client_rules_libraries_check,
 DROP COLUMN libraries,
 DROP CONSTRAINT client_rules_intent_check,
 ADD CONSTRAINT client_rules_intent_check CHECK((action IN ('observe','shadow'))=(intent IS NOT NULL) AND (intent IS NULL OR intent IN ('allow','deny','read_only','rate_limit','force_relogin'))),
 DROP CONSTRAINT client_rules_action_check,
 ADD CONSTRAINT client_rules_action_check CHECK(action IN ('allow','deny','read_only','rate_limit','force_relogin','observe','shadow'));
DELETE FROM users WHERE share_id IS NOT NULL;
ALTER TABLE users DROP CONSTRAINT users_share_guest_check, DROP COLUMN share_id;
DROP TABLE share_links;
DROP TABLE library_network_rules;
COMMIT;
