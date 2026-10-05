package postgres

import (
	"context"
	"fmt"
	"testing"
)

// G50.5: the readiness codes follow the schema and the job queue.
func TestReadinessReportsSchemaAndJobsPostgres(t *testing.T) {
	f := newJobFixture(t)
	want := func(expected map[string]string) {
		t.Helper()
		if got := f.s.Readiness(f.ctx); fmt.Sprint(got) != fmt.Sprint(expected) {
			t.Fatalf("readiness %v, want %v", got, expected)
		}
	}
	want(map[string]string{"database": "ok", "schema": "current", "jobs": "idle"})
	job := f.submit(t, "readiness")
	want(map[string]string{"database": "ok", "schema": "current", "jobs": "busy"})
	f.claim(t, "readiness-worker")
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '5 minutes' WHERE id=$1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	want(map[string]string{"database": "ok", "schema": "current", "jobs": "stalled"})
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET dirty=true`); err != nil {
		t.Fatal(err)
	}
	want(map[string]string{"database": "ok", "schema": "dirty", "jobs": "stalled"})
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET dirty=false,version=version-1`); err != nil {
		t.Fatal(err)
	}
	want(map[string]string{"database": "ok", "schema": "migration_required", "jobs": "stalled"})
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=version+2`); err != nil {
		t.Fatal(err)
	}
	want(map[string]string{"database": "ok", "schema": "newer", "jobs": "stalled"})
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=version-1`); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := f.s.Readiness(cancelled); got["database"] != "unavailable" || got["schema"] != "unknown" {
		t.Fatalf("cancelled readiness: %v", got)
	}
}
