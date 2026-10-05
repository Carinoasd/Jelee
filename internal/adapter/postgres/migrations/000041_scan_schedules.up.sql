CREATE TABLE scan_schedules (
    library_id uuid PRIMARY KEY REFERENCES libraries(id) ON DELETE CASCADE,
    owner_id uuid NOT NULL REFERENCES users(id),
    revision bigint NOT NULL CHECK (revision > 0),
    enabled boolean NOT NULL DEFAULT false,
    mode text NOT NULL CHECK (mode IN ('interval','cron')),
    interval_seconds integer NOT NULL,
    cron text NOT NULL CHECK (octet_length(cron) <= 256),
    timezone text NOT NULL CHECK (octet_length(timezone) BETWEEN 1 AND 128 AND timezone <> 'Local'),
    probe boolean NOT NULL,
    nfo boolean NOT NULL,
    ignore_mode text NOT NULL CHECK (ignore_mode IN ('','jeleeignore','jeleeignore-legacy-v1')),
    ignore_case text NOT NULL,
    CHECK ((ignore_mode='' AND ignore_case='') OR (ignore_mode<>'' AND ignore_case IN ('sensitive','ascii-insensitive'))),
    next_due timestamptz,
    retry_after timestamptz,
    last_job_id uuid REFERENCES jobs(id) ON DELETE SET NULL,
    last_error text NOT NULL DEFAULT '' CHECK (last_error IN ('','owner_unavailable','admission_unavailable')),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((mode='interval' AND interval_seconds BETWEEN 60 AND 31536000 AND cron='') OR (mode='cron' AND interval_seconds=0 AND octet_length(cron)>0)),
    CHECK (enabled = (next_due IS NOT NULL))
);
CREATE INDEX scan_schedules_due ON scan_schedules(next_due,library_id) WHERE enabled;
