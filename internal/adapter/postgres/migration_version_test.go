package postgres

import (
	"io/fs"
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
