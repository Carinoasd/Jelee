BEGIN;
-- G50.4 self-healing repairs. One row per executed repair (dry runs write
-- nothing). The result is the stable document of docs/repair.md; it holds
-- identifiers and root-relative paths only as the logging path mode allows,
-- never an absolute path. actor_id is NULL for the command line, which acts
-- with the database credentials of the host.
CREATE TABLE repair_runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 action text NOT NULL CONSTRAINT repair_runs_action_check CHECK(action IN ('items','image-variants','caches','stats','orphans','nfo','counts')),
 library_id uuid REFERENCES libraries(id) ON DELETE SET NULL,
 origin text NOT NULL CONSTRAINT repair_runs_origin_check CHECK(origin IN ('cli','api')),
 actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
 state text NOT NULL DEFAULT 'running' CONSTRAINT repair_runs_state_check CHECK(state IN ('running','completed','partial','failed')),
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 planned bigint NOT NULL DEFAULT 0 CONSTRAINT repair_runs_planned_check CHECK(planned>=0),
 applied bigint NOT NULL DEFAULT 0 CONSTRAINT repair_runs_applied_check CHECK(applied>=0),
 skipped bigint NOT NULL DEFAULT 0 CONSTRAINT repair_runs_skipped_check CHECK(skipped>=0),
 result jsonb CONSTRAINT repair_runs_result_check CHECK(jsonb_typeof(result)='object' AND octet_length(result::text)<=262144),
 CONSTRAINT repair_runs_finished_check CHECK((state='running')=(finished_at IS NULL)),
 CONSTRAINT repair_runs_result_state_check CHECK(state='running' OR result IS NOT NULL)
);
CREATE INDEX repair_runs_finished_idx ON repair_runs(finished_at DESC,id DESC) WHERE finished_at IS NOT NULL;

-- Every reversible repair with the exact values before and after it, so
-- `jelee-cli repair revert` restores them while the row still holds the
-- repaired value. The repairs are the journaled ones of the consistency
-- checker (schema 74), applied to every row instead of a sample. Journaled
-- runs are never trimmed.
CREATE TABLE repair_journal (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 run_id uuid NOT NULL REFERENCES repair_runs(id) ON DELETE RESTRICT,
 fix text NOT NULL CONSTRAINT repair_journal_fix_check CHECK(fix IN ('user_item_data.last_source_id','playback_sessions.source_id','watch_stats_daily.counters')),
 target jsonb NOT NULL CONSTRAINT repair_journal_target_check CHECK(jsonb_typeof(target)='object' AND octet_length(target::text)<=1024),
 before_state jsonb NOT NULL CONSTRAINT repair_journal_before_check CHECK(jsonb_typeof(before_state)='object' AND octet_length(before_state::text)<=1024),
 after_state jsonb NOT NULL CONSTRAINT repair_journal_after_check CHECK(jsonb_typeof(after_state)='object' AND octet_length(after_state::text)<=1024),
 applied_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 reverted_at timestamptz,
 CONSTRAINT repair_journal_revert_check CHECK(reverted_at IS NULL OR reverted_at>=applied_at)
);
CREATE INDEX repair_journal_run_idx ON repair_journal(run_id,id);
COMMIT;
