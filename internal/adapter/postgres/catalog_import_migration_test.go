package postgres

import "testing"

func TestCatalogImportSchemaAvailable(t *testing.T) {
	ctx, store, dsn := accountTestStore(t)
	var available bool
	if err := store.Pool.QueryRow(ctx, `SELECT to_regclass('catalog_import_requests') IS NOT NULL AND to_regclass('catalog_import_entries') IS NOT NULL`).Scan(&available); err != nil || !available {
		t.Fatal("durable catalog import schema unavailable", err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 41 {
		t.Fatal("empty catalog migration down", version, dirty, err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 40 {
		t.Fatal("empty catalog migration down", version, dirty, err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "down"); err != nil || dirty || version != 39 {
		t.Fatal("empty catalog migration down", version, dirty, err)
	}
	if version, dirty, err := Migrate(ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("catalog migration restore", version, dirty, err)
	}
}
