package postgres

import (
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// migrationVersion finds an embedded migration by name so retained-data
// tests do not depend on numbering, which changes when branches merge.
func migrationVersion(t *testing.T, name string) uint {
	t.Helper()
	matches, err := fs.Glob(migrationFiles, "migrations/*_"+name+".up.sql")
	if err != nil || len(matches) != 1 {
		t.Fatal("embedded migration not unique:", name)
	}
	number, _, _ := strings.Cut(strings.TrimPrefix(matches[0], "migrations/"), "_")
	version, err := strconv.ParseUint(number, 10, 32)
	if err != nil || version == 0 {
		t.Fatal("embedded migration has no version:", name)
	}
	return uint(version)
}

// downgradeAboveMigration rolls back every later schema, which must hold no
// retained data of its own, and returns the named migration's version. A
// following refused "down" then exercises exactly that migration's guard.
func downgradeAboveMigration(t *testing.T, f jobFixture, name string) uint {
	t.Helper()
	want := migrationVersion(t, name)
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	for err == nil && !dirty && version > want {
		version, dirty, err = Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down")
	}
	if err != nil || dirty || version != want {
		t.Fatalf("downgrade to %s: version=%d dirty=%t error=%v", name, version, dirty, err)
	}
	return want
}

// refuseRetainedDowngrade steps down one schema at a time until a migration
// refuses because the test's retained data still needs it. The refusal must
// come from the named migration or a later one that protects the same data;
// reaching below the named migration fails the test. It returns the dirty
// version left behind by the refused step.
func refuseRetainedDowngrade(t *testing.T, f jobFixture, name, message string) uint {
	t.Helper()
	want := migrationVersion(t, name)
	dsn := f.s.Pool.Config().ConnString()
	version, dirty, err := Migrate(f.ctx, dsn, "status")
	if err != nil || dirty {
		t.Fatalf("retained downgrade start: version=%d dirty=%t error=%v", version, dirty, err)
	}
	for version >= want {
		next, _, err := Migrate(f.ctx, dsn, "down")
		if err != nil {
			refused, dirty, statusErr := Migrate(f.ctx, dsn, "status")
			if statusErr != nil || !dirty || refused != version-1 {
				t.Fatalf("refused downgrade state: version=%d dirty=%t error=%v", refused, dirty, statusErr)
			}
			return refused
		}
		version = next
	}
	t.Fatal(message)
	return 0
}

// embeddedMigrationVersions lists the versions of the embedded migrations
// in ascending order.
func embeddedMigrationVersions(t *testing.T) []uint {
	t.Helper()
	matches, err := fs.Glob(migrationFiles, "migrations/*.up.sql")
	if err != nil || len(matches) == 0 {
		t.Fatal("no embedded migrations")
	}
	versions := make([]uint, 0, len(matches))
	for _, m := range matches {
		number, _, _ := strings.Cut(strings.TrimPrefix(m, "migrations/"), "_")
		version, err := strconv.ParseUint(number, 10, 32)
		if err != nil {
			t.Fatal("embedded migration has no version:", m)
		}
		versions = append(versions, uint(version))
	}
	slices.Sort(versions)
	return versions
}
