package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestDiagnosticsIntegration exercises the doctor pool against a fresh,
// isolated schema: unmigrated, current, dirty, and connection failures.
func TestDiagnosticsIntegration(t *testing.T) {
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required diagnostics integration database is unavailable")
		}
		t.Skip("diagnostics PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/jelee_test" || u.Hostname() == "" {
		t.Fatal("diagnostics integration requires dedicated jelee_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("cannot connect to configured test database; connection details are omitted")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	nonce := make([]byte, 10)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	schema := "jelee_diag_" + hex.EncodeToString(nonce)
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("cannot create isolated test schema")
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(c, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("could not remove isolated schema %s", schema)
		}
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	isolated := u.String()

	d, err := OpenDiagnostics(ctx, isolated)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if _, _, present, err := d.Migration(ctx); err != nil || present {
		t.Fatalf("unmigrated schema: present=%t err=%v", present, err)
	}
	if roots, err := d.LibraryRoots(ctx, 10); err != nil || len(roots) != 0 {
		t.Fatalf("roots before migration: %v %v", roots, err)
	}
	if jobs, err := d.JobSummary(ctx, time.Now().Add(-time.Hour), 10); err != nil || len(jobs) != 0 {
		t.Fatalf("jobs before migration: %v %v", jobs, err)
	}

	if version, dirty, err := Migrate(ctx, isolated, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("migrate: %d %t %v", version, dirty, err)
	}
	if version, dirty, present, err := d.Migration(ctx); err != nil || !present || dirty || version != SchemaVersion {
		t.Fatalf("migrated: %d %t %t %v", version, dirty, present, err)
	}
	// Triggers resolve unqualified names through the session search_path.
	if _, err := admin.Exec(ctx, "SET search_path TO "+quoted); err != nil {
		t.Fatal(err)
	}
	var libraryID string
	if err := admin.QueryRow(ctx, "INSERT INTO libraries(name) VALUES('Diag') RETURNING id::text").Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/srv/diag/a", "/srv/diag/b", "/srv/diag/c"} {
		if _, err := admin.Exec(ctx, "INSERT INTO library_roots(library_id,path) VALUES($1,$2)", libraryID, path); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := d.LibraryRoots(ctx, 2)
	if err != nil || len(roots) != 2 || roots[0].LibraryID != libraryID || !strings.HasPrefix(roots[0].Path, "/srv/diag/") || roots[0].ID == "" {
		t.Fatalf("bounded roots: %+v %v", roots, err)
	}
	tables, err := d.TableStats(ctx, 500)
	if err != nil || len(tables) < 50 {
		t.Fatalf("tables: %d %v", len(tables), err)
	}
	seen := map[string]bool{}
	for _, table := range tables {
		seen[table.Name] = true
		if table.TotalBytes < 0 || table.Rows < 0 {
			t.Fatalf("table %+v", table)
		}
	}
	if !seen["library_roots"] || !seen["jobs"] || !seen["schema_migrations"] {
		t.Fatal("expected tables missing from the current schema listing")
	}
	if limited, err := d.TableStats(ctx, 3); err != nil || len(limited) != 3 {
		t.Fatalf("limited tables: %d %v", len(limited), err)
	}
	if _, err := d.JobSummary(ctx, time.Now().Add(-time.Hour), 10); err != nil {
		t.Fatalf("job summary: %v", err)
	}
	// The diagnostics session is read-only.
	if _, err := d.pool.Exec(ctx, "DELETE FROM library_roots"); err == nil {
		t.Fatal("diagnostics session accepted a write")
	}

	if _, err := admin.Exec(ctx, "UPDATE schema_migrations SET dirty=true"); err != nil {
		t.Fatal(err)
	}
	if _, dirty, _, err := d.Migration(ctx); err != nil || !dirty {
		t.Fatalf("dirty not reported: %v", err)
	}

	wrong := *u
	if user := u.User.Username(); user != "" {
		wrong.User = url.UserPassword(user, "definitely-wrong-"+hex.EncodeToString(nonce[:4]))
		if _, err := OpenDiagnostics(ctx, wrong.String()); !errors.Is(err, ErrDiagnosticsAuth) {
			// Trust authentication accepts any password; only a rejection
			// can be asserted.
			if err != nil {
				t.Fatalf("wrong password: %v", err)
			}
			t.Log("server accepts any password (trust auth); auth rejection not asserted")
		}
	}
	unreachable := *u
	unreachable.Host = "127.0.0.1:1"
	if _, err := OpenDiagnostics(ctx, unreachable.String()); !errors.Is(err, ErrDiagnosticsUnavailable) {
		t.Fatalf("unreachable: %v", err)
	}
	if _, err := OpenDiagnostics(ctx, "postgres://%zz"); !errors.Is(err, ErrDiagnosticsConfig) {
		t.Fatalf("bad dsn: %v", err)
	}
}
