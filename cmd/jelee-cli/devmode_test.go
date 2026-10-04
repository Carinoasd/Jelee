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
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/jackc/pgx/v5"
)

func devmodeCLIConfig(env, enabled bool, environment string) config.Config {
	var cfg config.Config
	cfg.Dev = config.DevConfig{EnvFlag: env, Enabled: enabled, Environment: environment}
	return cfg
}

func devmodeCLIDeps(cfg config.Config, store devmode.Store, opened *int) devmodeCLIDependencies {
	return devmodeCLIDependencies{
		load: func() (config.Config, error) { return cfg, nil },
		open: func(context.Context, config.Config) (devmode.Store, func(), error) {
			*opened++
			return store, func() {}, nil
		},
	}
}

// devmodeServerToken issues a token the way a capable server's loopback
// entry does.
func devmodeServerToken(t *testing.T, store devmode.Store) string {
	t.Helper()
	server, err := devmode.NewController(devmode.ControllerOptions{Store: store, Local: devmode.Inputs{EnvFlag: true, ConfigEnabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := server.IssueToken(context.Background(), devmode.Actor{IP: "127.0.0.1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func runDevmode(t *testing.T, deps devmodeCLIDependencies, argv ...string) (int, devmodeCLIStatus, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := runDevmodeCLIWith(context.Background(), argv, &out, &errs, deps)
	var st devmodeCLIStatus
	if code == 0 {
		if err := json.Unmarshal(out.Bytes(), &st); err != nil {
			t.Fatalf("output %q", out.String())
		}
	}
	return code, st, errs.String()
}

func TestDevmodeCLIThresholds(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"no environment flag", devmodeCLIConfig(false, true, ""), "devmode_denied: missing env_flag"},
		{"no configuration switch", devmodeCLIConfig(true, false, ""), "devmode_denied: missing config_enabled"},
		{"production", devmodeCLIConfig(true, true, "production"), "devmode_production_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, opened := devmode.NewMemoryStore(), 0
			code, _, errs := runDevmode(t, devmodeCLIDeps(tc.cfg, store, &opened), "enable", "--token", devmodeServerToken(t, store))
			if code != 1 || !strings.HasPrefix(errs, tc.want) || opened != 0 {
				t.Fatalf("code=%d opened=%d stderr=%q", code, opened, errs)
			}
		})
	}
	t.Run("usage", func(t *testing.T) {
		store, opened := devmode.NewMemoryStore(), 0
		deps := devmodeCLIDeps(devmodeCLIConfig(true, true, ""), store, &opened)
		for _, argv := range [][]string{nil, {"enable"}, {"enable", "--token", "x", "--ttl", "25h"}, {"status", "extra"}, {"open"}} {
			if code, _, errs := runDevmode(t, deps, argv...); code != 2 || !strings.HasPrefix(errs, "usage:") {
				t.Errorf("%v: %d %q", argv, code, errs)
			}
		}
	})
}

func TestDevmodeCLIEnableStatusDisable(t *testing.T) {
	store, opened := devmode.NewMemoryStore(), 0
	deps := devmodeCLIDeps(devmodeCLIConfig(true, true, ""), store, &opened)
	if code, _, errs := runDevmode(t, deps, "enable", "--token", "jdm_forged"); code != 1 || !strings.HasPrefix(errs, "devmode_denied: the token") {
		t.Fatalf("forged token: %d %q", code, errs)
	}
	token := devmodeServerToken(t, store)
	code, st, errs := runDevmode(t, deps, "enable", "--token", token, "--ttl", "2h")
	if code != 0 || !st.Active || st.ExpiresAt.Sub(*st.EnabledAt) != 2*time.Hour || !strings.Contains(errs, "never use it in production") {
		t.Fatalf("enable: %d %+v %q", code, st, errs)
	}
	if code, _, errs = runDevmode(t, deps, "enable", "--token", token); code != 1 || !strings.HasPrefix(errs, "devmode_denied") {
		t.Fatalf("token reuse: %d %q", code, errs)
	}
	var enabled devmode.Event
	for _, a := range store.Audit() {
		if a.Event.Kind == devmode.EventEnabled {
			enabled = a.Event
		}
	}
	if enabled.Source != "cli" || !strings.Contains(enabled.ConfigDiff, "dev.enabled=true") || !strings.Contains(enabled.ConfigDiff, "ttl=2h0m0s") {
		t.Fatalf("enable audit: %+v", enabled)
	}
	if code, st, _ = runDevmode(t, deps, "status"); code != 0 || !st.Active {
		t.Fatalf("status: %d %+v", code, st)
	}
	// Disabling works from any process, even without the thresholds.
	plain := devmodeCLIDeps(devmodeCLIConfig(false, false, "production"), store, &opened)
	if code, st, _ = runDevmode(t, plain, "disable"); code != 0 || st.Active {
		t.Fatalf("disable: %d %+v", code, st)
	}
	if code, st, _ = runDevmode(t, plain, "disable"); code != 0 || st.Active {
		t.Fatalf("repeated disable: %d %+v", code, st)
	}
}

// The CLI against PostgreSQL: one token, one session, audited, disabled.
func TestDevmodeCLIPostgres(t *testing.T) {
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required developer mode CLI PostgreSQL integration is unavailable")
		}
		t.Skip("developer mode CLI PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Path != "/jelee_test" {
		t.Fatal("developer mode CLI integration requires the dedicated jelee_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect dedicated developer mode CLI test database")
	}
	defer admin.Close(context.Background())
	var random [10]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{"jelee_devmode_cli_it_" + hex.EncodeToString(random[:])}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("create owned developer mode CLI schema")
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("remove owned developer mode CLI schema")
		}
	}()
	query := u.Query()
	query.Set("search_path", strings.Trim(schema, `"`))
	u.RawQuery = query.Encode()
	if version, dirty, err := postgres.Migrate(ctx, u.String(), "up"); err != nil || dirty || version != postgres.SchemaVersion {
		t.Fatal("migrate developer mode CLI fixture")
	}
	store, err := postgres.Open(ctx, u.String(), 2)
	if err != nil {
		t.Fatal("open developer mode CLI store")
	}
	defer store.Pool.Close()
	opened := 0
	deps := devmodeCLIDeps(devmodeCLIConfig(true, true, "development"), store, &opened)
	if code, st, errs := runDevmode(t, deps, "enable", "--token", devmodeServerToken(t, store)); code != 0 || !st.Active {
		t.Fatalf("enable: %d %+v %q", code, st, errs)
	}
	if code, st, errs := runDevmode(t, deps, "disable"); code != 0 || st.Active {
		t.Fatalf("disable: %d %+v %q", code, st, errs)
	}
	var events int
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE event IN ('devmode.token_issued','devmode.enabled','devmode.disabled') AND category='security'`).Scan(&events); err != nil || events != 3 {
		t.Fatalf("audit rows: %d %v", events, err)
	}
}
