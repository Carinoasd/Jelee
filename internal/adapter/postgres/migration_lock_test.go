package postgres

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/migrationlock"
)

// TestMigrationLockCoversEmbeddedMigrations is the source guard of G04.2:
// every embedded migration is recorded in migrations/checksums.txt with its
// current SHA-256, so a released migration cannot change, disappear or be
// renumbered, and a new one must be locked (go run ./tools/migrationlock
// -update). It needs no database.
func TestMigrationLockCoversEmbeddedMigrations(t *testing.T) {
	embedded, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	files, err := migrationlock.Files(embedded)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("migrations", migrationlock.FileName))
	if err != nil {
		t.Fatalf("read the migration lock: %v", err)
	}
	lock, err := migrationlock.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	appendable, violations := migrationlock.Check(lock, files)
	for _, v := range violations {
		t.Error(v)
	}
	for _, e := range appendable {
		t.Errorf("%s is not locked; run `go run ./tools/migrationlock -update` and commit %s", e.Name, migrationlock.FileName)
	}
	if len(lock) != len(files) {
		t.Errorf("lock holds %d files, %d migrations are embedded", len(lock), len(files))
	}
}
