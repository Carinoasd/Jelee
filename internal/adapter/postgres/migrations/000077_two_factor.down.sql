BEGIN;
-- The previous schema has no second factor: dropping an active enrollment would let
-- the account sign in with its password alone. The downgrade refuses until
-- every enrollment is reset (DELETE /api/v1/users/{id}/two-factor) or the
-- users disabled it. Pending enrollments, challenges and application
-- passwords are dropped; sessions an application password issued stay valid
-- until they expire or are revoked, like any other session.
LOCK TABLE user_totp IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM user_totp WHERE enabled_at IS NOT NULL) THEN
  RAISE EXCEPTION 'reset two-factor enrollments before downgrade';
 END IF;
END $$;
DROP INDEX sessions_app_password_idx;
ALTER TABLE sessions DROP COLUMN app_password_id;
DROP TABLE app_passwords;
DROP TABLE login_challenges;
DROP TABLE user_recovery_codes;
DROP TABLE user_totp;
COMMIT;
