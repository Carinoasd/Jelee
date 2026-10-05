BEGIN;
LOCK TABLE users, user_item_access_rules, user_blocked_tags, access_policy IN ACCESS EXCLUSIVE MODE;
-- Schema 68 filters by library grants only. Dropping configured content
-- restrictions would silently show what they hide, so they block the
-- downgrade; removing them first is an explicit operator decision.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM user_item_access_rules) OR EXISTS(SELECT 1 FROM user_blocked_tags)
  OR EXISTS(SELECT 1 FROM users WHERE parental_rating_max IS NOT NULL) THEN
  RAISE EXCEPTION 'configured content access rules prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE user_blocked_tags;
DROP TABLE user_item_access_rules;
DROP FUNCTION mark_user_content_filtered();
ALTER TABLE users DROP CONSTRAINT users_content_filtered_check,
 DROP COLUMN content_filtered, DROP COLUMN block_unrated, DROP COLUMN parental_rating_max;
DROP TABLE parental_ratings;
DROP TABLE access_policy;
COMMIT;
