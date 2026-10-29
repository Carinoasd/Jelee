BEGIN;
-- Version preferences and session track choices only order versions and
-- pick starting tracks; schema 98 falls back to the main version, the
-- quality score and the stored track preferences.
DROP TABLE session_track_selections;
DROP TABLE user_version_preferences;
COMMIT;
