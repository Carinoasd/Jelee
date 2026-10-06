package postgres

import (
	"context"
)

// Readiness reports the state of the database dependencies as fixed codes
// for /readyz (G50.5): never a version number, count, address or error
// text, so the codes are safe to show to anyone who can reach the probe.
func (s *Store) Readiness(ctx context.Context) map[string]string {
	checks := map[string]string{"database": "unavailable", "schema": "unknown", "jobs": "unknown"}
	if ctx == nil || s == nil || s.Pool == nil || s.Pool.Ping(ctx) != nil {
		return checks
	}
	checks["database"] = "ok"
	var version int
	var dirty bool
	var stalled, active int64
	// A running job whose lease ran out has lost its worker; the lease index
	// serves the first count, the active library index the second.
	if err := s.Pool.QueryRow(ctx, `SELECT version,dirty,
 (SELECT count(*) FROM jobs WHERE state='running' AND lease_until<clock_timestamp()-interval '1 minute'),
 (SELECT count(*) FROM jobs WHERE state IN ('queued','running')) FROM schema_migrations`).Scan(&version, &dirty, &stalled, &active); err != nil {
		if err = s.Pool.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
			return checks
		}
	} else {
		switch {
		case stalled > 0:
			checks["jobs"] = "stalled"
		case active > 0:
			checks["jobs"] = "busy"
		default:
			checks["jobs"] = "idle"
		}
	}
	switch {
	case dirty:
		checks["schema"] = "dirty"
	case version < SchemaVersion:
		checks["schema"] = "migration_required"
	case version > SchemaVersion:
		checks["schema"] = "newer"
	default:
		checks["schema"] = "current"
	}
	return checks
}
