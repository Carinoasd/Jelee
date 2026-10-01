package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Integration runs only against an explicitly named test database. Every run
// owns a new random schema; neither public nor an existing schema is modified.
// CI must set JELEE_REQUIRE_INTEGRATION=true so missing configuration is fatal.
func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required PostgreSQL integration unavailable: set JELEE_TEST_DATABASE_URL to a dedicated jelee_test database")
		}
		t.Skip("PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL is unset; this skip is not database validation")
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/jelee_test" || u.Hostname() == "" {
		t.Fatal("integration is restricted to an explicit PostgreSQL URL whose database name is jelee_test")
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
		t.Fatal("cannot generate isolated test schema name")
	}
	schema := "jelee_it_" + hex.EncodeToString(nonce)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal("cannot create isolated test schema; test account needs CREATE permission")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("could not remove this run's isolated schema %s", schema)
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	isolatedDSN := u.String()
	t.Logf("PostgreSQL integration RUNNING: dedicated jelee_test database, isolated schema %s", schema)

	for _, step := range []struct {
		action  string
		version uint
	}{{"up", SchemaVersion}, {"status", SchemaVersion}, {"down", 16}, {"down", 15}, {"down", 14}, {"down", 13}, {"down", 12}, {"down", 11}, {"down", 10}, {"down", 9}, {"down", 8}, {"down", 7}, {"down", 6}, {"down", 5}, {"down", 4}, {"down", 3}, {"down", 2}, {"down", 1}, {"down", 0}, {"up", SchemaVersion}, {"up", SchemaVersion}} {
		version, dirty, err := Migrate(ctx, isolatedDSN, step.action)
		if err != nil || dirty || version != step.version {
			t.Fatalf("migration %s: version=%d dirty=%t failed=%t", step.action, version, dirty, err != nil)
		}
	}
	store, err := Open(ctx, isolatedDSN, 4)
	if err != nil {
		t.Fatal("migrated test schema failed readiness")
	}
	t.Cleanup(store.Pool.Close)
	var currentSchema string
	if err := store.Pool.QueryRow(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil || currentSchema != schema {
		t.Fatal("test connection is not confined to its owned schema")
	}
	t.Run("migration_lock_contention_is_bounded", func(t *testing.T) {
		key, err := database.GenerateAdvisoryLockId("jelee_test", schema, "schema_migrations")
		if err != nil {
			t.Fatal("derive migration advisory lock")
		}
		lockID, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			t.Fatal("parse migration advisory lock")
		}
		if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
			t.Fatal("acquire test migration lock")
		}
		var unlockOnce sync.Once
		unlock := func() {
			unlockOnce.Do(func() {
				unlockCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				if _, err := admin.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", lockID); err != nil {
					t.Error("release test migration lock")
				}
			})
		}
		defer unlock()
		migrationCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		defer stop()
		result := make(chan error, 1)
		started := time.Now()
		go func() {
			_, _, err := Migrate(migrationCtx, isolatedDSN, "up")
			result <- err
		}()
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("contended migration unexpectedly succeeded")
			}
			t.Logf("contended migration returned after %s; server-side timeout bounds the driver's non-cancelable setup", time.Since(started))
		case <-time.After(8 * time.Second):
			unlock()
			select {
			case <-result:
			case <-time.After(3 * time.Second):
				t.Error("migration did not recover after test lock release")
			}
			t.Fatal("migration exceeded advisory lock timeout; caller cancellation alone does not bound the driver")
		}
	})

	provision := func(name string, kind access.ClientKind, isAdmin bool) (string, access.Principal) {
		t.Helper()
		token, err := store.Provision(ctx, name, kind, isAdmin)
		if err != nil || len(token) != 43 {
			t.Fatalf("provision %s failed or returned malformed token", name)
		}
		principal, err := store.Authenticate(ctx, token)
		if err != nil || principal.Kind != kind || principal.Admin != isAdmin || !domain.ValidID(principal.UserID) || !domain.ValidID(principal.SessionID) {
			t.Fatalf("provisioned %s cannot authenticate with expected server-side identity", name)
		}
		return token, principal
	}
	nativeToken, native := provision("native", access.ClientNative, false)
	_, web := provision("web", access.ClientWeb, false)
	_, administrator := provision("administrator", access.ClientNative, true)
	disabledToken, disabled := provision("disabled", access.ClientNative, false)

	t.Run("credentials_are_hashed_and_lifecycle_enforced", func(t *testing.T) {
		var stored []byte
		var active bool
		if err := store.Pool.QueryRow(ctx, "SELECT token_hash,expires_at>now() AND expires_at<=now()+interval '24 hours' FROM sessions WHERE id=$1", native.SessionID).Scan(&stored, &active); err != nil {
			t.Fatal("read stored session hash")
		}
		expected := sha256.Sum256([]byte(nativeToken))
		if !bytes.Equal(stored, expected[:]) || bytes.Equal(stored, []byte(nativeToken)) || !active {
			t.Fatal("session must contain only token digest and a bounded expiration")
		}
		for _, token := range []string{"short", strings.Repeat("x", 43)} {
			if _, err := store.Authenticate(ctx, token); !errors.Is(err, domain.ErrUnauthenticated) {
				t.Error("invalid token was not rejected as unauthenticated")
			}
		}
		if _, err := store.Pool.Exec(ctx, "UPDATE users SET disabled=true WHERE id=$1", disabled.UserID); err != nil {
			t.Fatal("disable test user")
		}
		if _, err := store.Authenticate(ctx, disabledToken); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatal("disabled user could authenticate")
		}
		before := integrationCounts(t, ctx, store)
		if _, err := store.Provision(ctx, "native", access.ClientNative, false); err == nil {
			t.Fatal("duplicate user creation unexpectedly succeeded")
		}
		if after := integrationCounts(t, ctx, store); before != after {
			t.Fatalf("failed provisioning left partial state: before %v after %v", before, after)
		}
	})

	firstSource, err := store.ImportVideo(ctx, "Visible", "/synthetic/visible", "one.mp4", "First", "video/mp4")
	if err != nil {
		t.Fatal("import visible synthetic item")
	}
	secondSource, err := store.ImportVideo(ctx, "Hidden", "/synthetic/hidden", "two.mkv", "Second", "video/x-matroska")
	if err != nil {
		t.Fatal("import hidden synthetic item")
	}
	var firstItem, secondItem, firstLibrary, secondLibrary, secondRoot string
	if err := store.Pool.QueryRow(ctx, "SELECT item_id::text,library_id::text FROM media_sources WHERE id=$1", firstSource).Scan(&firstItem, &firstLibrary); err != nil {
		t.Fatal("read first synthetic source identity")
	}
	if err := store.Pool.QueryRow(ctx, "SELECT item_id::text,library_id::text,root_id::text FROM media_sources WHERE id=$1", secondSource).Scan(&secondItem, &secondLibrary, &secondRoot); err != nil {
		t.Fatal("read second synthetic source identity")
	}
	for _, user := range []string{native.UserID, web.UserID, disabled.UserID} {
		if _, err := store.Pool.Exec(ctx, "INSERT INTO library_acl(user_id,library_id) VALUES($1,$2)", user, firstLibrary); err != nil {
			t.Fatal("grant scoped test ACL")
		}
	}

	t.Run("ACL_filters_lists_details_and_streams_in_SQL", func(t *testing.T) {
		items, err := store.ListItems(ctx, native.UserID, "", 100)
		if err != nil || len(items) != 1 || items[0].ID != firstItem {
			t.Fatalf("native ACL list leaked or lost items: count=%d failed=%t", len(items), err != nil)
		}
		if item, err := store.GetItem(ctx, native.UserID, firstItem); err != nil || item.ID != firstItem {
			t.Fatal("authorized item not visible")
		}
		if _, err := store.GetItem(ctx, native.UserID, secondItem); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("hidden item did not use opaque not-found semantics")
		}
		if source, err := store.Resolve(ctx, native, firstSource); err != nil || source.RelativePath != "one.mp4" {
			t.Fatal("authorized native stream was not resolved")
		}
		if _, err := store.Resolve(ctx, native, secondSource); !errors.Is(err, media.ErrNotFound) {
			t.Fatal("hidden source escaped ACL filter")
		}
		spoofedWeb := web
		spoofedWeb.Kind = access.ClientNative
		if _, err := store.Resolve(ctx, spoofedWeb, firstSource); !errors.Is(err, media.ErrNotFound) {
			t.Fatal("repository trusted caller's client-kind instead of persisted Web session")
		}
		if items, err := store.ListItems(ctx, administrator.UserID, "", 100); err != nil || len(items) != 2 {
			t.Fatal("administrator catalog policy mismatch")
		}
		if items, err := store.ListItems(ctx, disabled.UserID, "", 100); err != nil || len(items) != 0 {
			t.Fatal("disabled user remains visible through list SQL")
		}
		if _, err := store.GetItem(ctx, disabled.UserID, firstItem); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("disabled user remains visible through detail SQL")
		}
	})

	t.Run("constraints_and_failed_imports_are_atomic", func(t *testing.T) {
		before := integrationCounts(t, ctx, store)
		for _, input := range []struct{ library, root, relative, contentType string }{
			{"Visible", "/synthetic/visible", "one.mp4", "video/mp4"},
			{"Visible", "/synthetic/visible", "invalid.exe", "application/x-executable"},
			{"RejectedNewLibrary", "/synthetic/visible", "other.mp4", "video/mp4"},
		} {
			if _, err := store.ImportVideo(ctx, input.library, input.root, input.relative, "Rejected", input.contentType); err == nil {
				t.Error("invalid import unexpectedly succeeded")
			}
			if after := integrationCounts(t, ctx, store); after != before {
				t.Fatalf("failed import created partial catalog/audit state: before %v after %v", before, after)
			}
		}
		_, err := store.Pool.Exec(ctx, "UPDATE media_sources SET root_id=$1 WHERE id=$2", secondRoot, firstSource)
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "23503" {
			t.Fatal("cross-library source/root mismatch was not rejected by foreign key")
		}
		_, err = store.Pool.Exec(ctx, "INSERT INTO items(library_id,title,kind) VALUES($1,'Unsupported','Audio')", firstLibrary)
		if !errors.As(err, &pgError) || pgError.Code != "23514" {
			t.Fatal("non-video item kind was not rejected by CHECK")
		}
	})

	t.Run("ten_thousand_items_plan_and_bounded_visible_page", func(t *testing.T) {
		if _, err := store.Pool.Exec(ctx, "INSERT INTO items(library_id,title,kind) SELECT $1::uuid,'Synthetic '||n,'Movie' FROM generate_series(1,10000) n", secondLibrary); err != nil {
			t.Fatal("seed 10000 synthetic metadata rows")
		}
		if _, err := store.Pool.Exec(ctx, "ANALYZE items"); err != nil {
			t.Fatal("analyze synthetic metadata")
		}
		items, err := store.ListItems(ctx, native.UserID, "", 100)
		if err != nil || len(items) != 1 || items[0].ID != firstItem {
			t.Fatal("large invisible library leaked into authorized page")
		}
		adminPage, err := store.ListItems(ctx, administrator.UserID, "", 50)
		if err != nil || len(adminPage) != 50 {
			t.Fatal("database pagination did not enforce bound")
		}
		nextPage, err := store.ListItems(ctx, administrator.UserID, adminPage[len(adminPage)-1].ID, 50)
		if err != nil || len(nextPage) != 50 || nextPage[0].ID <= adminPage[len(adminPage)-1].ID {
			t.Fatal("cursor page overlaps or is not in stable UUID order")
		}
		const explain = "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) " + listItemsSQL
		var rawPlan []byte
		if err := store.Pool.QueryRow(ctx, explain, native.UserID, "", 50).Scan(&rawPlan); err != nil {
			t.Fatal("collect SQL permission-filter query plan")
		}
		var plan []map[string]any
		if err := json.Unmarshal(rawPlan, &plan); err != nil || len(plan) != 1 {
			t.Fatal("EXPLAIN did not return one JSON execution plan")
		}
		if !strings.Contains(string(rawPlan), "Index") || !strings.Contains(string(rawPlan), "Limit") {
			t.Fatal("bounded catalog plan did not use an index and limit")
		}
		scannedItems := countPlanItemRows(plan[0])
		if scannedItems < 1 || scannedItems > 50 {
			t.Fatalf("restricted-user plan visited %.0f item rows; it must not scan the 10,001-row hidden library", scannedItems)
		}
		t.Logf("restricted-user execution visited %.0f item rows; hidden-library scan regression baseline was 10,002", scannedItems)
		t.Logf("10,002-row ACL query plan (single sample; no P95 or performance acceptance claimed): %s", rawPlan)
	})

	t.Run("revocation_and_cancellation_apply_before_media_lookup", func(t *testing.T) {
		if _, err := store.Pool.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE id=$1", native.SessionID); err != nil {
			t.Fatal("revoke test session")
		}
		if _, err := store.Authenticate(ctx, nativeToken); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatal("revoked token can authenticate")
		}
		if _, err := store.Resolve(ctx, native, firstSource); !errors.Is(err, media.ErrNotFound) {
			t.Fatal("stale principal bypassed session revocation")
		}
		canceled, stop := context.WithCancel(ctx)
		stop()
		if _, err := store.ListItems(canceled, administrator.UserID, "", 50); !errors.Is(err, context.Canceled) {
			t.Fatal("query did not honor canceled context")
		}
	})
}

// Count actual work on the items relation, including discarded rows. An index
// node name by itself is insufficient: an ordered index can still scan every
// hidden item before the ACL predicate removes it.
func countPlanItemRows(value any) float64 {
	switch v := value.(type) {
	case map[string]any:
		total := float64(0)
		if v["Relation Name"] == "items" {
			loops, _ := v["Actual Loops"].(float64)
			for _, name := range []string{"Actual Rows", "Rows Removed by Filter", "Rows Removed by Index Recheck"} {
				rows, _ := v[name].(float64)
				total += rows * loops
			}
		}
		for _, child := range v {
			total += countPlanItemRows(child)
		}
		return total
	case []any:
		total := float64(0)
		for _, child := range v {
			total += countPlanItemRows(child)
		}
		return total
	}
	return 0
}

func integrationCounts(t *testing.T, ctx context.Context, store *Store) [7]int {
	t.Helper()
	var counts [7]int
	err := store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM sessions),(SELECT count(*) FROM libraries),(SELECT count(*) FROM library_roots),(SELECT count(*) FROM items),(SELECT count(*) FROM media_sources),(SELECT count(*) FROM audit_logs)`).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6])
	if err != nil {
		t.Fatal("count isolated test rows")
	}
	return counts
}
