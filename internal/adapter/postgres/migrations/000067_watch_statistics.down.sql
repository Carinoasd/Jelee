BEGIN;
LOCK TABLE watch_stats_daily, watch_stats_history, playback_sessions IN ACCESS EXCLUSIVE MODE;
-- Schema 66 has no roll-up, and the daily rows outlive the sessions they
-- were computed from, so retained statistics block the downgrade like the
-- playback history of schema 66 does.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM watch_stats_daily) OR EXISTS(SELECT 1 FROM watch_stats_history) THEN
  RAISE EXCEPTION 'retained watch statistics prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP INDEX playback_sessions_stats_pending_idx;
ALTER TABLE playback_sessions
 DROP CONSTRAINT playback_sessions_stats_counted_check,
 DROP COLUMN stats_through,
 DROP COLUMN stats_day,
 DROP COLUMN stats_counted,
 DROP COLUMN stats_completed,
 DROP COLUMN stats_completion_milli,
 DROP COLUMN stats_play;
DROP TABLE watch_stats_history;
DROP TABLE watch_stats_daily;
COMMIT;
