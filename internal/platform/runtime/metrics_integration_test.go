package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/jackc/pgx/v5"
)

// This fixture supplies the trusted KDF result through the real account store.
// Password hashing is covered separately; no session row or token is invented.
const metricsIntegrationHash = "$argon2id$v=19$m=8192,t=1,p=1$c2FsdC1maXh0dXJl$dGVzdC1kaWdlc3QtZml4dHVyZQ"

func metricsIntegrationStore(t *testing.T) (context.Context, *postgres.Store, *pgx.Conn, string, string) {
	t.Helper()
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required metrics integration database is unavailable")
		}
		t.Skip("metrics runtime/PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/jelee_test" || u.Hostname() == "" {
		t.Fatal("metrics integration requires dedicated jelee_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect dedicated metrics test database")
	}
	t.Cleanup(func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := admin.Close(closeCtx); err != nil {
			t.Error("close metrics schema owner connection")
		}
	})
	var random [10]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal("generate metrics schema identifier")
	}
	schema := "jelee_metrics_it_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("create owned metrics test schema")
	}
	t.Cleanup(func() {
		cleanCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("remove owned metrics test schema")
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	if version, dirty, err := postgres.Migrate(ctx, u.String(), "up"); err != nil || dirty || version != postgres.SchemaVersion {
		t.Fatal("migrate owned metrics test schema")
	}
	store, err := postgres.Open(ctx, u.String(), 4)
	if err != nil {
		t.Fatal("open metrics fixture store")
	}
	t.Cleanup(store.Pool.Close)
	applicationName := schema + "_runtime"
	query.Set("application_name", applicationName)
	u.RawQuery = query.Encode()
	return ctx, store, admin, u.String(), applicationName
}

func metricsIntegrationLogin(t *testing.T, ctx context.Context, store *postgres.Store, name string) domain.SessionGrant {
	t.Helper()
	credentials, err := store.Credentials(ctx, name)
	if err != nil {
		t.Fatal("read metrics fixture credentials")
	}
	grant, err := store.CommitLogin(ctx, domain.LoginInput{
		Credentials: credentials, PasswordOK: true, DeviceName: "metrics-integration",
		IP: "127.0.0.1", MaxSessions: 8, SessionTTL: time.Hour, LockAfter: 3, LockFor: time.Minute,
	})
	if err != nil {
		t.Fatal("commit metrics fixture login")
	}
	return grant
}

func metricsIntegrationValue(t *testing.T, body, name, metricType string) float64 {
	t.Helper()
	if !strings.Contains(body, "# TYPE "+name+" "+metricType+"\n") {
		t.Fatalf("missing exported metric type for %s", name)
	}
	found := false
	var value float64
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != name {
			continue
		}
		if found || len(fields) != 2 {
			t.Fatalf("unexpected duplicate or labeled metric %s", name)
		}
		var err error
		value, err = strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			t.Fatalf("invalid exported metric value for %s", name)
		}
		found = true
	}
	if !found {
		t.Fatalf("missing exported metric sample for %s", name)
	}
	return value
}

func TestMetricsRuntimePostgresIntegration(t *testing.T) {
	ctx, store, observer, runtimeDSN, applicationName := metricsIntegrationStore(t)
	input := domain.UserInput{Name: "MetricsAdmin", DisplayName: "Metrics admin", Locale: "en-US", PasswordHash: metricsIntegrationHash}
	if user, err := store.BootstrapAdmin(ctx, input); err != nil || !user.Admin {
		t.Fatal("bootstrap metrics administrator")
	}
	admin := metricsIntegrationLogin(t, ctx, store, input.Name)
	actor := domain.Actor{UserID: admin.User.ID, SessionID: admin.Session.ID, IP: "127.0.0.1"}
	input.Name, input.DisplayName = "MetricsUser", "Metrics user"
	if user, replayed, err := store.CreateUser(ctx, actor, input, "metrics-regular-user"); err != nil || replayed || user.Admin {
		t.Fatal("create ordinary metrics user")
	}
	regular := metricsIntegrationLogin(t, ctx, store, input.Name)
	values := map[string]string{
		"JELEE_DATABASE_URL": runtimeDSN, "JELEE_ENABLE_ACCOUNTS": "true",
		"JELEE_ENABLE_METRICS": "true", "JELEE_MAX_CONNECTIONS": "7",
	}
	cfg, err := config.LoadWith(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil || !cfg.EnableMetrics || !cfg.EnableAccounts {
		t.Fatal("load production metrics configuration")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	life := newLifetime(logger)
	address := ""
	life.listen = func(listenCtx context.Context, network, _ string) (net.Listener, error) {
		listener, err := (&net.ListenConfig{}).Listen(listenCtx, network, "127.0.0.1:0")
		if err == nil {
			address = listener.Addr().String()
		}
		return listener, err
	}
	application := newWithLifetime(cfg, logger, life)
	if application.Err() != nil {
		t.Fatal("build production metrics runtime")
	}
	startup, stopStartup := context.WithTimeout(ctx, 15*time.Second)
	err = application.Start(startup)
	stopStartup()
	if err != nil {
		t.Fatal("start production metrics runtime")
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := application.Stop(stopCtx); err != nil {
			t.Error("stop production metrics runtime")
		}
		stopped = true
	}
	t.Cleanup(stop)
	if address == "" || life.closeTelemetry == nil {
		t.Fatal("runtime did not own listener and metrics lifecycle")
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	endpoint := "http://" + address + "/metrics"
	scrape := func(token string, want int) string {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatal("construct metrics request")
		}
		request.Header.Set("Accept", "text/plain; version=0.0.4")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("request production metrics endpoint")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
		if err != nil || len(body) > 64<<10 {
			t.Fatal("read bounded metrics response")
		}
		if response.StatusCode != want {
			t.Fatalf("metrics response status=%d, want %d", response.StatusCode, want)
		}
		if want == http.StatusOK {
			if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain") || response.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("metrics exposition headers are missing")
			}
		} else if strings.Contains(string(body), "jelee_runtime_") || strings.Contains(string(body), "jelee_db_pool_") || strings.Contains(string(body), "jelee_resources_") {
			t.Fatal("unauthorized response exposed metrics")
		}
		return string(body)
	}
	scrape("", http.StatusUnauthorized)
	scrape(regular.Token, http.StatusForbidden)
	body := scrape(admin.Token, http.StatusOK)
	for name, want := range map[string]int{
		"cpu_limit": cfg.Resources.CPULimit(), "io_limit": cfg.Resources.IO,
		"total_limit": cfg.Resources.Total, "queue_limit": cfg.Resources.Queue,
	} {
		if metricsIntegrationValue(t, body, "jelee_resources_"+name, "gauge") != float64(want) {
			t.Fatalf("resource metric %s differs from runtime configuration", name)
		}
	}
	if metricsIntegrationValue(t, body, "jelee_db_pool_connections_max", "gauge") != float64(cfg.MaxConnections) {
		t.Fatal("exporter was not wired to the configured production pool")
	}
	if metricsIntegrationValue(t, body, "jelee_runtime_heap_bytes", "gauge") <= 0 || metricsIntegrationValue(t, body, "jelee_runtime_goroutines", "gauge") < 1 {
		t.Fatal("exporter was not wired to live runtime measurements")
	}
	if metricsIntegrationValue(t, body, "jelee_runtime_allocated_bytes_total", "counter") <= 0 {
		t.Fatal("runtime allocation counter is missing")
	}
	metricsIntegrationValue(t, body, "jelee_runtime_gc_pause_seconds_total", "counter")
	for _, secret := range []string{runtimeDSN, admin.Token, regular.Token, admin.User.ID, regular.User.ID, applicationName} {
		if strings.Contains(body, secret) {
			t.Fatal("metrics response contains private fixture information")
		}
	}
	if err := store.RevokeSession(ctx, actor, admin.User.ID, admin.Session.ID); err != nil {
		t.Fatal("revoke metrics administrator session")
	}
	scrape(admin.Token, http.StatusUnauthorized)
	stop()
	select {
	case <-life.stopped:
	default:
		t.Fatal("runtime shutdown did not finish resource cleanup")
	}
	if life.ctx.Err() == nil {
		t.Fatal("runtime lifetime was not canceled")
	}
	if response, err := client.Get(endpoint); err == nil {
		response.Body.Close()
		t.Fatal("metrics listener remained available after Stop")
	}
	// Backend exit can trail TCP close briefly. Observe only this runtime's
	// random application_name, leaving every other test and database user alone.
	closed, cancelClosed := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelClosed()
	for {
		var connections int
		if err := observer.QueryRow(closed, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1`, applicationName).Scan(&connections); err != nil {
			t.Fatal("verify production metrics pool cleanup")
		}
		if connections == 0 {
			break
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-closed.Done():
			timer.Stop()
			t.Fatal("production metrics pool connections survived Stop")
		case <-timer.C:
		}
	}
}
