BEGIN;
-- Session expiry changes no row, so session.ended for an expired session
-- (G12.1) is raised by a sweep that walks sessions in expiry order. This
-- single row is how far the sweep got: (through_at, through_id) is the last
-- session covered, through_id NULL covering every session that expired at or
-- before through_at. It starts now, so the upgrade raises nothing for
-- sessions that expired before it.
CREATE TABLE session_expiry_watermark (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 through_at timestamptz NOT NULL,
 through_id uuid
);
INSERT INTO session_expiry_watermark(through_at) VALUES(now());
CREATE INDEX sessions_expiry_idx ON sessions(expires_at,id);
COMMIT;
