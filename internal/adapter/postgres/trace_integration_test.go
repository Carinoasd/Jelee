package postgres

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
)

type traceLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *traceLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *traceLogBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("log record is not JSON: %v", err)
		}
		out = append(out, record)
	}
	return out
}

func installTracer(t *testing.T, rate float64, logs *traceLogBuffer) {
	t.Helper()
	tracer, err := tracing.New(tracing.Options{SampleRate: rate, Logger: logging.New(logs), Links: tracing.NewLinks(64)})
	if err != nil {
		t.Fatal(err)
	}
	previous := tracing.SetDefault(tracer)
	t.Cleanup(func() { tracing.SetDefault(previous) })
}

// TestTraceHTTPScanJobPostgres (G46.6): a scan submitted over HTTP is run by
// the job worker in the trace of the submitting request. The request ID is
// the trace ID of every record: the access log, the worker's job records
// and the span records of the job and its scan stage.
func TestTraceHTTPScanJobPostgres(t *testing.T) {
	logs := &traceLogBuffer{}
	installTracer(t, 1, logs)
	logger := logging.New(logs)
	f := newJobFixture(t)
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	service, err := app.NewJobs(f.s, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(f.s, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Resources: config.DefaultResourcesConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test", MaxConnections: 16, MaxStreams: 2, RequestTimeoutSeconds: 5, EnableAccounts: true, Accounts: config.DefaultAccountsConfig(), EnableJobs: true, Jobs: config.DefaultJobsConfig()}
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, logger, accounts, service)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://localhost/api/v1/libraries/"+f.registration.Library.ID+"/scan", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+grant.Token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "trace-scan")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal("scan admission", w.Code, w.Body)
	}
	requestID := w.Header().Get("X-Request-ID")
	var job struct {
		Data domain.Job `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &job) != nil || !domain.ValidID(job.Data.ID) {
		t.Fatal("invalid job response")
	}
	opts := jobs.DefaultOptions()
	opts.Workers = 1
	runner, err := jobs.New(f.s, scan.New(), opts, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for f.get(t, job.Data.ID).State != domain.JobSucceeded {
		if time.Now().After(deadline) {
			t.Fatal("scan job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runner.Stop(stop); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, record := range logs.records(t) {
		msg, _ := record["msg"].(string)
		key := msg
		if msg == "span completed" {
			key, _ = record["span"].(string)
		}
		switch key {
		case "request completed":
			if record["requestId"] != requestID {
				continue
			}
		case "job started", "inventory job completed":
			if record["taskId"] != job.Data.ID {
				continue
			}
		case "http.request", "job.inventory_scan", "scan.inventory":
		default:
			continue
		}
		if record["traceId"] != requestID {
			t.Fatalf("%s record left the request trace: %v", key, record)
		}
		if span, _ := record["spanId"].(string); len(span) != 16 {
			t.Fatalf("%s record has no span: %v", key, record)
		}
		if key == "job.inventory_scan" && (record["linked"] != true || record["linkKind"] != tracing.LinkJob || record["parentSpanId"] == nil) {
			t.Fatalf("job span not linked to its submitter: %v", record)
		}
		found[key] = true
	}
	for _, key := range []string{"request completed", "job started", "inventory job completed", "http.request", "job.inventory_scan", "scan.inventory"} {
		if !found[key] {
			t.Fatalf("no %q record in the request trace", key)
		}
	}
}

// TestTraceSecurityEventsBypassSamplingPostgres (G46.6): with sampling off,
// ordinary requests write no span record, but a failed login (a security
// audit row) forces its request trace to be kept.
func TestTraceSecurityEventsBypassSamplingPostgres(t *testing.T) {
	logs := &traceLogBuffer{}
	installTracer(t, 0, logs)
	logger := logging.New(logs)
	ctx, s, _ := accountTestStore(t)
	if _, err := s.BootstrapAdmin(ctx, accountInput("trace-admin")); err != nil {
		t.Fatal(err)
	}
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(s, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Resources: config.DefaultResourcesConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test", MaxConnections: 16, MaxStreams: 2, RequestTimeoutSeconds: 5, EnableAccounts: true, Accounts: config.DefaultAccountsConfig()}
	handler, err := httpapi.NewWithJobs(cfg, s, app.NewCatalog(s), s, logger, accounts, nil)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(method, path, body string) string {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Header().Get("X-Request-ID")
	}
	ordinary := serve("GET", "/api/v1/openapi.json", "")
	failed := serve("POST", "/api/v1/auth/login", `{"name":"trace-admin","password":"wrong password"}`)
	var audited int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE event='login.failed' AND category='security'`).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("failed login not audited as a security event: %d %v", audited, err)
	}
	spans := map[string]map[string]any{}
	for _, record := range logs.records(t) {
		if record["msg"] == "span completed" {
			trace, _ := record["traceId"].(string)
			spans[trace] = record
		}
	}
	if _, ok := spans[ordinary]; ok {
		t.Fatal("an unsampled ordinary request wrote a span record")
	}
	record, ok := spans[failed]
	if !ok || record["forced"] != true || record["span"] != "http.request" {
		t.Fatalf("failed login trace dropped by sampling: %v", record)
	}
}
