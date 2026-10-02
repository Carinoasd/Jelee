package postgres

import (
	"context"
	"testing"
)

// legacyMigrationAt44 selects the schema where pre-metrics migration contracts
// were defined. Call it before a fixture submits or starts work; schema45 must
// never discard durable observations just to let an older migration test pass.
// Ordinary fixtures omit this hook and continue to exercise the latest schema.
func legacyMigrationAt44(t *testing.T, f jobFixture) {
	t.Helper()
	legacyMigrationStoreAt44(t, f.ctx, f.s)
}

func legacyMigrationStoreAt44(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	var version int
	var dirty bool
	if err := store.Pool.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil || dirty || version != 45 {
		t.Fatal("legacy migration fixture requires a clean schema45 before downgrading", version, dirty, err)
	}
	actual, dirty, err := Migrate(ctx, store.Pool.Config().ConnString(), "down")
	if err != nil || dirty || actual != 44 {
		t.Fatal("legacy migration fixture cannot discard recorded job metrics", actual, dirty, err)
	}
}
