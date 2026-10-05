BEGIN;
LOCK TABLE playback_sessions, playback_samples, user_item_data IN ACCESS EXCLUSIVE MODE;
-- Schema 65 has nowhere to keep watch history, resume points or played
-- states, which are user data, so retained rows block the downgrade.
-- Clearing them is an explicit operator decision, not a side effect.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM playback_sessions) OR EXISTS(SELECT 1 FROM user_item_data) THEN
  RAISE EXCEPTION 'retained playback history prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE playback_samples;
DROP TABLE playback_sessions;
DROP TABLE user_item_data;
COMMIT;
