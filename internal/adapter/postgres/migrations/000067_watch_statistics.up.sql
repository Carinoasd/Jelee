BEGIN;
-- Watch statistics roll-up (G23.3, G23.5). Ended playback sessions are
-- aggregated once, incrementally, into daily rows of user, item and local
-- day; statistics requests read only these rows, never the sample table.
-- The rows outlive the retention purge of sessions and samples, and go with
-- their user and item. A cleared history or a deleted user removes them in
-- the same transaction as the sessions.
CREATE TABLE watch_stats_daily (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 day date NOT NULL CONSTRAINT watch_stats_daily_day_check CHECK(day BETWEEN DATE '1970-01-01' AND DATE '9999-12-31'),
 item_id uuid NOT NULL,
 library_id uuid NOT NULL,
 effective_ms bigint NOT NULL DEFAULT 0 CONSTRAINT watch_stats_daily_effective_check CHECK(effective_ms BETWEEN 0 AND 90000000),
 sessions integer NOT NULL DEFAULT 0 CONSTRAINT watch_stats_daily_sessions_check CHECK(sessions>=0),
 views integer NOT NULL DEFAULT 0 CONSTRAINT watch_stats_daily_views_check CHECK(views>=0),
 first_plays integer NOT NULL DEFAULT 0 CONSTRAINT watch_stats_daily_first_plays_check CHECK(first_plays>=0),
 rewatches integer NOT NULL DEFAULT 0 CONSTRAINT watch_stats_daily_rewatches_check CHECK(rewatches>=0),
 completions integer NOT NULL DEFAULT 0 CONSTRAINT watch_stats_daily_completions_check CHECK(completions>=0),
 completion_milli bigint NOT NULL DEFAULT 0 CONSTRAINT watch_stats_daily_completion_check CHECK(completion_milli>=0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,day,item_id),
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE,
 -- Only columns that every delta row satisfies on its own: CHECK applies to
 -- the proposed row of INSERT ... ON CONFLICT before the merge, and a
 -- re-aggregated session adds a completion without a session.
 CONSTRAINT watch_stats_daily_views_sum_check CHECK(views=first_plays+rewatches AND views<=sessions)
);
-- Statistics of every user and the export read a day range; per user
-- statistics use the primary key. The item index serves the cascade.
CREATE INDEX watch_stats_daily_day_idx ON watch_stats_daily(day,user_id,item_id);
CREATE INDEX watch_stats_daily_item_idx ON watch_stats_daily(item_id);

-- First play and re-watch classification state per user and item.
CREATE TABLE watch_stats_history (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 views integer NOT NULL DEFAULT 0 CONSTRAINT watch_stats_history_views_check CHECK(views>=0),
 completions integer NOT NULL DEFAULT 0 CONSTRAINT watch_stats_history_completions_check CHECK(completions>=0),
 last_completed boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,item_id)
);
CREATE INDEX watch_stats_history_item_idx ON watch_stats_history(item_id);

-- What the aggregation counted for each session. stats_through is the end
-- the aggregation read: a session is pending while it ended after that (it
-- was never aggregated, or a client rejoined it after a timeout and it
-- ended again). Sessions of schema 66 start pending and are aggregated by
-- the first runs.
ALTER TABLE playback_sessions
 ADD COLUMN stats_through timestamptz CONSTRAINT playback_sessions_stats_through_check CHECK(isfinite(stats_through)),
 ADD COLUMN stats_day date,
 ADD COLUMN stats_counted boolean NOT NULL DEFAULT false,
 ADD COLUMN stats_completed boolean NOT NULL DEFAULT false,
 ADD COLUMN stats_completion_milli integer NOT NULL DEFAULT 0 CONSTRAINT playback_sessions_stats_completion_check CHECK(stats_completion_milli BETWEEN 0 AND 1000),
 ADD COLUMN stats_play text CONSTRAINT playback_sessions_stats_play_check CHECK(stats_play IN ('none','first','continue','rewatch')),
 ADD CONSTRAINT playback_sessions_stats_counted_check CHECK(NOT stats_counted OR stats_day IS NOT NULL AND stats_through IS NOT NULL);
CREATE INDEX playback_sessions_stats_pending_idx ON playback_sessions(ended_at,id)
 WHERE ended_at IS NOT NULL AND (stats_through IS NULL OR stats_through<ended_at);
COMMIT;
