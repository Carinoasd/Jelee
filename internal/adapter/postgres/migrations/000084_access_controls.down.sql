BEGIN;
LOCK TABLE users, user_blocked_keywords, user_access_windows, access_templates, access_template_libraries IN ACCESS EXCLUSIVE MODE;
-- Schema 83 knows neither keywords nor time windows: dropping them would
-- silently show what they hide. Templates are administrator configuration
-- an older binary could not list. All of them block the downgrade;
-- removing them first is an explicit operator decision.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM user_blocked_keywords) OR EXISTS(SELECT 1 FROM user_access_windows) OR EXISTS(SELECT 1 FROM access_templates) THEN
  RAISE EXCEPTION 'configured keywords, time windows or access templates prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE access_template_libraries;
DROP TABLE access_templates;
DROP TABLE user_access_windows;
DROP TABLE user_blocked_keywords;
COMMIT;
