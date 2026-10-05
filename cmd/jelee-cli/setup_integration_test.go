package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/jackc/pgx/v5"
)

// setupCLIDatabase migrates an owned schema in the dedicated jelee_test
// database and returns its DSN.
func setupCLIDatabase(t *testing.T) (context.Context, string) {
	t.Helper()
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required setup CLI integration database is unavailable")
		}
		t.Skip("setup CLI PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || u.Path != "/jelee_test" || u.Hostname() == "" {
		t.Fatal("setup CLI integration requires dedicated jelee_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect dedicated setup CLI test database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var random [10]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	quoted := pgx.Identifier{"jelee_setup_cli_" + hex.EncodeToString(random[:])}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("create owned setup CLI schema")
	}
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(clean, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("remove owned setup CLI schema")
		}
	})
	query := u.Query()
	query.Set("search_path", strings.Trim(quoted, `"`))
	u.RawQuery = query.Encode()
	if version, dirty, err := postgres.Migrate(ctx, u.String(), "up"); err != nil || dirty || version != postgres.SchemaVersion {
		t.Fatal("migrate setup CLI schema")
	}
	return ctx, u.String()
}

func setupCLIConfig(dsn string) func() (config.Config, error) {
	return func() (config.Config, error) {
		accounts := config.DefaultAccountsConfig()
		accounts.PasswordMemoryKiB, accounts.PasswordIterations = int(password.MinMemoryKiB), int(password.MinIterations)
		cfg := config.Config{Resources: config.DefaultResourcesConfig(), Access: config.DefaultAccessConfig(), Streaming: config.DefaultStreamingConfig(),
			Playback: config.DefaultPlaybackConfig(), Stats: config.DefaultStatsConfig(), Logging: config.DefaultLoggingConfig(),
			Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: dsn, MaxConnections: 4, MaxStreams: 4, RequestTimeoutSeconds: 15,
			EnableAccounts: true, Accounts: accounts}
		return cfg, cfg.Validate()
	}
}

// G18.5: `jelee-cli setup --non-interactive` initialises an empty database
// through the same PostgreSQL wizard the server uses, a rerun is refused
// without side effects, and the server would find setup complete.
func TestSetupCLIHeadlessPostgres(t *testing.T) {
	ctx, dsn := setupCLIDatabase(t)
	media := t.TempDir()
	argv := setupBaseArgs("--locale", "ja-JP", "--library", "Movies="+media, "--accept-degraded-tools", "--listen", "127.0.0.1:8097")
	run := func() (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := runSetupCLIWith(ctx, argv, strings.NewReader(setupCLIPassword+"\n"), &stdout, &stderr, setupCLIDependencies{open: openPostgresSetup(setupCLIConfig(dsn))})
		return code, stdout.String(), stderr.String()
	}
	code, stdout, stderr := run()
	if code != 0 || stderr != "" {
		t.Fatalf("headless setup: code=%d stderr=%q", code, stderr)
	}
	var state domain.SetupState
	if err := json.Unmarshal([]byte(stdout), &state); err != nil || !state.Completed() || state.Locale != "ja-JP" || len(state.Media) != 1 || strings.Contains(stdout, setupCLIPassword) {
		t.Fatalf("headless output: %q %v", stdout, err)
	}
	store, err := postgres.Open(ctx, dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Pool.Close()
	var admins, libraries, audits int
	if err = store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users WHERE is_admin AND name='admin' AND locale='ja-JP'),
		(SELECT count(*) FROM libraries l JOIN library_roots r ON r.library_id=l.id WHERE l.name='Movies' AND r.path=$1 AND l.metadata_language='ja-JP'),
		(SELECT count(*) FROM audit_logs WHERE event='setup.completed' AND after_state->>'channel'='cli')`, media).Scan(&admins, &libraries, &audits); err != nil {
		t.Fatal(err)
	}
	if admins != 1 || libraries != 1 || audits != 1 {
		t.Fatalf("records: admins=%d libraries=%d audits=%d", admins, libraries, audits)
	}
	// A second run (container restart) is refused and changes nothing.
	if code, stdout, stderr = run(); code != 1 || stderr != "setup_already_completed\n" || stdout != "" {
		t.Fatalf("rerun: code=%d stderr=%q", code, stderr)
	}
	var users int
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil || users != 1 {
		t.Fatalf("rerun created users: %d %v", users, err)
	}
}
