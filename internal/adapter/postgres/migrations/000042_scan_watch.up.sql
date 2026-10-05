ALTER TABLE scan_schedules ADD COLUMN watch_enabled boolean NOT NULL DEFAULT false;
CREATE INDEX scan_schedules_last_job ON scan_schedules(last_job_id) WHERE last_job_id IS NOT NULL;
CREATE TABLE scan_watch_state (
    library_id uuid PRIMARY KEY REFERENCES scan_schedules(library_id) ON DELETE CASCADE,
    lease_owner uuid,
    observing boolean NOT NULL DEFAULT false,
    lease_generation bigint NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    definition_revision bigint NOT NULL DEFAULT 0 CHECK (definition_revision >= 0),
    inventory_generation bigint NOT NULL DEFAULT 0 CHECK (inventory_generation >= 0),
    lease_until timestamptz,
    dirty_generation bigint NOT NULL DEFAULT 0 CHECK (dirty_generation >= 0),
    accepted_generation bigint NOT NULL DEFAULT 0 CHECK (accepted_generation >= 0 AND accepted_generation <= dirty_generation),
    retry_after timestamptz,
    observe_after timestamptz,
    last_job_id uuid REFERENCES jobs(id) ON DELETE SET NULL,
    last_error text NOT NULL DEFAULT '' CHECK (last_error IN ('','observer_unavailable','resource_limit','admission_unavailable')),
    CHECK ((lease_owner IS NULL) = (lease_until IS NULL))
);
CREATE INDEX scan_watch_last_job ON scan_watch_state(last_job_id) WHERE last_job_id IS NOT NULL;
