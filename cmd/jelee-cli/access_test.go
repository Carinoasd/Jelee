package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/jackc/pgx/v5"
)

type accessStoreStub struct {
	result domain.ClientPolicyReset
	err    error
	resets int
}

func (s *accessStoreStub) ResetClientPolicies(context.Context) (domain.ClientPolicyReset, error) {
	s.resets++
	return s.result, s.err
}

func accessDeps(store accessCLIStore, openErr error, closed *int) accessCLIDependencies {
	return accessCLIDependencies{
		load: func() (config.Config, error) {
			return config.Config{DatabaseURL: "postgres://private:secret@localhost/database", MaxConnections: 2}, nil
		},
		open: func(context.Context, config.Config) (accessCLIStore, func(), error) {
			if openErr != nil {
				return nil, nil, openErr
			}
			return store, func() { *closed++ }, nil
		},
	}
}

func TestAccessCLIResetPolicies(t *testing.T) {
	store := &accessStoreStub{result: domain.ClientPolicyReset{RulesDisabled: 3, Policy: domain.ClientPolicy{UnknownClients: "allow", ExemptAdmins: true, ExemptLoopback: true, Version: 9}}}
	closed := 0
	var out, errs bytes.Buffer
	if code := runAccessCLIWith(context.Background(), []string{"reset-policies"}, &out, &errs, accessDeps(store, nil, &closed)); code != 0 || store.resets != 1 || closed != 1 || errs.Len() != 0 {
		t.Fatalf("code=%d resets=%d closed=%d stderr=%q", code, store.resets, closed, errs.String())
	}
	var printed domain.ClientPolicyReset
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil || printed.RulesDisabled != 3 || printed.Policy.UnknownClients != "allow" {
		t.Fatalf("output %q", out.String())
	}
}

func TestAccessCLIRejectsUsageAndHidesFailures(t *testing.T) {
	for _, argv := range [][]string{nil, {"reset"}, {"reset-policies", "extra"}, {"reset-policies", "--force"}} {
		var out, errs bytes.Buffer
		if code := runAccessCLIWith(context.Background(), argv, &out, &errs, accessDeps(&accessStoreStub{}, nil, new(int))); code != 2 || out.Len() != 0 || !strings.HasPrefix(errs.String(), "usage:") {
			t.Errorf("%v: code=%d stderr=%q", argv, code, errs.String())
		}
	}
	var out, errs bytes.Buffer
	if code := runAccessCLIWith(context.Background(), []string{"reset-policies"}, &out, &errs, accessDeps(nil, errors.New("dial postgres://private:secret@localhost"), new(int))); code != 1 || strings.Contains(errs.String(), "secret") || strings.TrimSpace(errs.String()) != "access_database_unavailable" {
		t.Fatalf("open failure: code=%d stderr=%q", code, errs.String())
	}
	errs.Reset()
	store := &accessStoreStub{err: errors.New("relation client_rules: private detail")}
	if code := runAccessCLIWith(context.Background(), []string{"reset-policies"}, &out, &errs, accessDeps(store, nil, new(int))); code != 1 || strings.TrimSpace(errs.String()) != "access_reset_failed" {
		t.Fatalf("reset failure: code=%d stderr=%q", code, errs.String())
	}
}

// G47.7 on PostgreSQL: a rule set that refuses every client, administrators
// and loopback included, is undone by the command.
func TestAccessCLIResetPoliciesPostgres(t *testing.T) {
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required access CLI PostgreSQL integration is unavailable")
		}
		t.Skip("access CLI PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Path != "/jelee_test" {
		t.Fatal("access CLI integration requires the dedicated jelee_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect dedicated access CLI test database")
	}
	defer admin.Close(context.Background())
	var random [10]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{"jelee_access_cli_it_" + hex.EncodeToString(random[:])}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("create owned access CLI schema")
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("remove owned access CLI schema")
		}
	}()
	query := u.Query()
	query.Set("search_path", strings.Trim(schema, `"`))
	u.RawQuery = query.Encode()
	if version, dirty, err := postgres.Migrate(ctx, u.String(), "up"); err != nil || dirty || version != postgres.SchemaVersion {
		t.Fatal("migrate access CLI fixture")
	}
	store, err := postgres.Open(ctx, u.String(), 2)
	if err != nil {
		t.Fatal("open access CLI store")
	}
	defer store.Pool.Close()
	if _, err = store.Pool.Exec(ctx, `INSERT INTO client_rules(dimension,match_kind,pattern,action) VALUES('user_agent','glob','*','deny'),('ip','cidr','0.0.0.0/0','deny');
 UPDATE client_control_policy SET unknown_clients='deny',exempt_admins=false,exempt_loopback=false,version=version+1`); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	deps := accessCLIDependencies{
		load: func() (config.Config, error) { return config.Config{}, nil },
		open: func(context.Context, config.Config) (accessCLIStore, func(), error) { return store, func() {}, nil },
	}
	if code := runAccessCLIWith(ctx, []string{"reset-policies"}, &out, &errs, deps); code != 0 {
		t.Fatalf("reset exited %d: %s", code, errs.String())
	}
	state, err := store.ClientControlState(ctx)
	if err != nil || len(state.Rules) != 0 || state.Policy.UnknownClients != "allow" || !state.Policy.ExemptAdmins || !state.Policy.ExemptLoopback || state.Policy.Version != 3 {
		t.Fatalf("state after reset: %+v %v", state, err)
	}
	var disabled, audited int
	if err = store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM client_rules WHERE NOT enabled),(SELECT count(*) FROM audit_logs WHERE event='client_control.policies_reset')`).Scan(&disabled, &audited); err != nil || disabled != 2 || audited != 1 {
		t.Fatalf("disabled=%d audited=%d %v", disabled, audited, err)
	}
	if !strings.Contains(out.String(), `"rulesDisabled":2`) {
		t.Fatalf("output %q", out.String())
	}
}
