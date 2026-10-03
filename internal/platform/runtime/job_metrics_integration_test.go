package runtime

import (
	"context"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/jackc/pgx/v5"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type jobMetricsRuntimeFixture struct {
	endpoint       string
	client         *http.Client
	stop           func()
	resourceValues map[string]float64
}

func startJobMetricsRuntime(t *testing.T, ctx context.Context, observer *pgx.Conn, dsn, suffix string, maxConnections int) jobMetricsRuntimeFixture {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("parse owned runtime database URL")
	}
	query := u.Query()
	applicationName := query.Get("application_name") + "_" + suffix
	query.Set("application_name", applicationName)
	u.RawQuery = query.Encode()
	values := map[string]string{
		"JELEE_DATABASE_URL": u.String(), "JELEE_ENABLE_ACCOUNTS": "true",
		"JELEE_ENABLE_METRICS": "true", "JELEE_ENABLE_JOBS": "false",
		"JELEE_MAX_CONNECTIONS": strconv.Itoa(maxConnections),
	}
	cfg, err := config.LoadWith(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil || cfg.EnableJobs || !cfg.EnableMetrics {
		t.Fatal("load production shared metrics configuration")
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
		t.Fatal("build production shared metrics runtime")
	}
	startup, cancelStartup := context.WithTimeout(ctx, 15*time.Second)
	err = application.Start(startup)
	cancelStartup()
	if err != nil {
		t.Fatal("start production shared metrics runtime")
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	fixture := jobMetricsRuntimeFixture{endpoint: "http://" + address + "/metrics", client: &http.Client{Transport: transport, Timeout: 5 * time.Second}}
	fixture.resourceValues = map[string]float64{
		"cpu_active": 0, "io_active": 0, "total_active": 0, "waiting": 0,
		"cpu_limit": float64(cfg.Resources.CPULimit()), "io_limit": float64(cfg.Resources.IO),
		"total_limit": float64(cfg.Resources.Total), "queue_limit": float64(cfg.Resources.Queue),
	}
	stopped := false
	fixture.stop = func() {
		if stopped {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := application.Stop(stopCtx); err != nil {
			t.Error("stop production shared metrics runtime")
		}
		stopped = true
		select {
		case <-life.stopped:
		default:
			t.Error("shared metrics runtime did not finish cleanup")
		}
		if life.ctx.Err() == nil {
			t.Error("shared metrics runtime lifetime was not cancelled")
		}
		if response, err := fixture.client.Get(fixture.endpoint); err == nil {
			response.Body.Close()
			t.Error("shared metrics listener remained available after stop")
		}
		closed, cancelClosed := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelClosed()
		for {
			var connections int
			if err := observer.QueryRow(closed, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1`, applicationName).Scan(&connections); err != nil {
				t.Error("verify shared metrics runtime pool cleanup")
				return
			}
			if connections == 0 {
				return
			}
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-closed.Done():
				timer.Stop()
				t.Error("shared metrics runtime connections survived stop")
				return
			case <-timer.C:
			}
		}
	}
	t.Cleanup(fixture.stop)
	if address == "" || life.closeTelemetry == nil || life.worker != nil {
		t.Fatal("shared metrics runtime requires its own listener/exporter and no worker")
	}
	return fixture
}

func (f jobMetricsRuntimeFixture) request(t *testing.T, ctx context.Context, token, query, host string, want int) string {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, f.endpoint+query, nil)
	if err != nil {
		t.Fatal("construct shared metrics request")
	}
	r.Header.Set("Accept", "text/plain; version=0.0.4")
	r.Header.Set("X-Forwarded-User", "admin")
	r.Header.Set("X-Admin", "true")
	r.Header.Set("X-Forwarded-Host", "localhost")
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if host != "" {
		r.Host = host
	}
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal("request bounded shared metrics endpoint")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		t.Fatal("read bounded shared metrics response")
	}
	if response.StatusCode != want {
		t.Fatalf("shared metrics status=%d, want %d", response.StatusCode, want)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("shared metrics bypassed the HTTP boundary")
	}
	if want == http.StatusOK {
		if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain;") {
			t.Fatal("shared metrics did not return exporter exposition")
		}
	} else if strings.Contains(string(body), "jelee_jobs_") || strings.Contains(string(body), "jelee_runtime_") || strings.Contains(string(body), "jelee_db_pool_") || strings.Contains(string(body), "jelee_resources_") {
		t.Fatal("failed shared metrics request exposed partial data")
	}
	return string(body)
}

func sharedMetricsPoint(t *testing.T, families map[string]*dto.MetricFamily, name, kind, priority, outcome string, typ dto.MetricType) *dto.Metric {
	t.Helper()
	family := families[name]
	wantLabels, wantPoints := 2, 8
	if outcome != "" {
		wantLabels, wantPoints = 3, 24
	}
	if family == nil || family.GetType() != typ || len(family.Metric) != wantPoints {
		t.Fatal("missing fixed shared metrics family", name)
	}
	for _, point := range family.Metric {
		labels := make(map[string]string)
		for _, label := range point.Label {
			labels[label.GetName()] = label.GetValue()
		}
		if len(point.Label) == wantLabels && len(labels) == wantLabels && labels["kind"] == kind && labels["priority"] == priority && labels["outcome"] == outcome {
			return point
		}
	}
	t.Fatal("missing fixed shared metrics labels", name, kind, priority, outcome)
	return nil
}

func scrapeSharedMetricsSnapshot(t *testing.T, ctx context.Context, store *postgres.Store, runtime jobMetricsRuntimeFixture, token string, maxConnections int) (app.JobMetricsSnapshot, string) {
	t.Helper()
	before, err := store.JobMetrics(ctx)
	if err != nil {
		t.Fatal("read source before shared metrics request")
	}
	body := runtime.request(t, ctx, token, "", "", http.StatusOK)
	after, err := store.JobMetrics(ctx)
	if err != nil || !before.StartedAt.Equal(after.StartedAt) {
		t.Fatal("read stable metrics source epoch after request")
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		t.Fatal("parse production metrics exposition")
	}
	// 30 runtime, pool, job, and resource families plus 7 cache families for
	// the HTTP server's OpenAPI cache in the process-wide cache registry.
	if len(families) != 37 {
		t.Fatalf("production metrics exposed %d families, want 37", len(families))
	}
	for _, name := range []string{"jelee_cache_hits_total", "jelee_cache_misses_total", "jelee_cache_evictions_total", "jelee_cache_expirations_total", "jelee_cache_entries", "jelee_cache_size_bytes", "jelee_cache_hit_ratio"} {
		family := families[name]
		if family == nil || len(family.Metric) != 1 || len(family.Metric[0].Label) != 1 || family.Metric[0].Label[0].GetName() != "cache" || family.Metric[0].Label[0].GetValue() != "http.openapi" {
			t.Fatalf("production cache metric %s must have exactly the fixed cache label", name)
		}
	}
	// Workers are disabled in this fixture. Scraping must not acquire work
	// permits, and each instance must expose its configured resource limits.
	for suffix, want := range runtime.resourceValues {
		name := "jelee_resources_" + suffix
		family := families[name]
		if family == nil || family.GetType() != dto.MetricType_GAUGE || len(family.Metric) != 1 || len(family.Metric[0].Label) != 0 || family.Metric[0].Gauge == nil || family.Metric[0].GetGauge().GetValue() != want {
			t.Fatalf("production resource metric %s must be one unlabeled gauge with value %v", name, want)
		}
	}
	if metricsIntegrationValue(t, body, "jelee_db_pool_connections_max", "gauge") != float64(maxConnections) {
		t.Fatal("shared metrics contaminated the local pool dimensions")
	}
	for i, group := range after.Groups {
		for name, value := range map[string]int64{
			"jelee_jobs_shared_queued": group.Queued, "jelee_jobs_shared_running": group.Running, "jelee_jobs_shared_expired_running": group.ExpiredRunning,
		} {
			point := sharedMetricsPoint(t, families, name, group.Kind, group.Priority, "", dto.MetricType_GAUGE)
			if point.GetGauge().GetValue() != float64(value) {
				t.Fatal("HTTP shared gauge differs from PostgreSQL snapshot", name)
			}
		}
		age := sharedMetricsPoint(t, families, "jelee_jobs_shared_oldest_queued_age_seconds", group.Kind, group.Priority, "", dto.MetricType_GAUGE).GetGauge().GetValue()
		if math.IsNaN(age) || age < before.Groups[i].OldestQueuedAgeSeconds-1e-6 || age > group.OldestQueuedAgeSeconds+1e-6 {
			t.Fatal("HTTP oldest queued age is outside the surrounding DB snapshots")
		}
		for outcome, value := range map[string]int64{"succeeded": group.Succeeded, "failed": group.Failed, "cancelled": group.Cancelled} {
			point := sharedMetricsPoint(t, families, "jelee_jobs_shared_outcomes_total", group.Kind, group.Priority, outcome, dto.MetricType_COUNTER)
			if point.GetCounter().GetValue() != float64(value) {
				t.Fatal("HTTP shared outcome differs from PostgreSQL snapshot", outcome)
			}
		}
		for _, measure := range []struct {
			name   string
			value  app.JobMetricHistogram
			bounds [12]float64
		}{{"jelee_jobs_shared_initial_wait_seconds", group.Wait, app.JobWaitBoundsSeconds()}, {"jelee_jobs_shared_duration_seconds", group.Duration, app.JobDurationBoundsSeconds()}} {
			hist := sharedMetricsPoint(t, families, measure.name, group.Kind, group.Priority, "", dto.MetricType_HISTOGRAM).GetHistogram()
			if hist == nil || hist.GetSampleCount() != measure.value.Count || hist.GetSampleSum() != measure.value.SumSeconds || len(hist.Bucket) != 13 {
				t.Fatal("HTTP histogram count/sum differs from PostgreSQL snapshot", measure.name)
			}
			var cumulative uint64
			for bucket, point := range hist.Bucket {
				bound := math.Inf(1)
				if bucket < len(measure.bounds) {
					bound = measure.bounds[bucket]
				}
				cumulative += measure.value.BucketCounts[bucket]
				if point.GetUpperBound() != bound || point.GetCumulativeCount() != cumulative {
					t.Fatal("HTTP histogram bucket differs from PostgreSQL snapshot", measure.name, bucket)
				}
			}
		}
	}
	return after, body
}

func jobMetricsRuntimeAccount(t *testing.T, ctx context.Context, store *postgres.Store) (domain.SessionGrant, domain.Actor) {
	t.Helper()
	input := domain.UserInput{Name: "SharedMetricsAdmin", DisplayName: "Shared metrics admin", Locale: "en-US", PasswordHash: metricsIntegrationHash}
	if _, err := store.BootstrapAdmin(ctx, input); err != nil {
		t.Fatal("bootstrap shared metrics administrator")
	}
	grant := metricsIntegrationLogin(t, ctx, store, input.Name)
	return grant, domain.Actor{UserID: grant.User.ID, SessionID: grant.Session.ID, IP: "127.0.0.1"}
}

func TestJobMetricsRuntimePostgresLifecycleAndInstances(t *testing.T) {
	ctx, store, observer, dsn, applicationName := metricsIntegrationStore(t)
	admin, actor := jobMetricsRuntimeAccount(t, ctx, store)
	rootPath := t.TempDir()
	library, err := store.RegisterLibrary(ctx, "shared-metrics-library", rootPath)
	if err != nil {
		t.Fatal("register shared metrics job library")
	}
	policy := domain.JobPolicy{QueueLimit: 8, HistoryLimit: 1, MaxEntries: 1000, MaxDirectories: 100, MaxAttempts: 3, MissingCountLimit: 10, MissingPercentLimit: 50}
	one := startJobMetricsRuntime(t, ctx, observer, dsn, "one", 4)
	two := startJobMetricsRuntime(t, ctx, observer, dsn, "two", 7)
	initial, _ := scrapeSharedMetricsSnapshot(t, ctx, store, one, admin.Token, 4)
	submit := func(key, priority string) domain.Job {
		t.Helper()
		job, replayed, err := store.SubmitJob(ctx, actor, library.Library.ID, key, priority, policy)
		if err != nil || replayed {
			t.Fatal("submit shared metrics inventory job")
		}
		return job
	}
	claim := func(owner string) domain.JobLease {
		t.Helper()
		lease, err := store.ClaimJob(ctx, owner, false, time.Minute)
		if err != nil {
			t.Fatal("claim shared metrics inventory job")
		}
		return lease
	}
	finish := func(lease domain.JobLease) {
		t.Helper()
		directory, err := store.NextScanDirectory(ctx, lease)
		if err != nil {
			t.Fatal("read shared metrics inventory directory")
		}
		batch := domain.ScanBatch{Done: true, Entries: []domain.InventoryEntry{{RootID: directory.RootID, Path: "source-fixture.mkv", Kind: "video", Size: 7, ModifiedUnixNano: 123456789}}}
		if err := store.SaveScanBatch(ctx, lease, directory, batch); err != nil {
			t.Fatal("save real shared metrics inventory checkpoint")
		}
		if err := store.FinishJob(ctx, lease, domain.JobSucceeded, ""); err != nil {
			t.Fatal("publish real shared metrics inventory job")
		}
	}
	first := submit("shared-metrics-first", domain.JobPriorityManual)
	queued, _ := scrapeSharedMetricsSnapshot(t, ctx, store, one, admin.Token, 4)
	if group := queued.Groups[5]; group.Queued != 1 || group.Running != 0 || group.Wait.Count != 0 {
		t.Fatal("submitted job did not appear as queued before its first claim")
	}
	lease := claim("shared-metrics-first-owner")
	running, _ := scrapeSharedMetricsSnapshot(t, ctx, store, two, admin.Token, 7)
	if group := running.Groups[5]; group.Queued != 0 || group.Running != 1 || group.Wait.Count != 1 || group.Duration.Count != 0 {
		t.Fatal("first claim did not expose exactly one initial wait")
	}
	if err := store.ReleaseJob(ctx, lease); err != nil {
		t.Fatal("release shared metrics job for another owner")
	}
	requeued, _ := scrapeSharedMetricsSnapshot(t, ctx, store, one, admin.Token, 4)
	if group := requeued.Groups[5]; group.Queued != 1 || group.Running != 0 || group.Wait != running.Groups[5].Wait {
		t.Fatal("release replayed an initial wait sample")
	}
	lease = claim("shared-metrics-next-owner")
	finish(lease)
	finished, _ := scrapeSharedMetricsSnapshot(t, ctx, store, two, admin.Token, 7)
	if group := finished.Groups[5]; group.Queued != 0 || group.Running != 0 || group.Succeeded != 1 || group.Wait != running.Groups[5].Wait || group.Duration.Count != 1 {
		t.Fatal("successful reclaim did not expose one completion and one duration")
	}
	submit("shared-metrics-second", domain.JobPriorityManual)
	finish(claim("shared-metrics-second-owner"))
	var retained, old int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE id=$1::uuid) FROM jobs`, first.ID).Scan(&retained, &old); err != nil || retained != 1 || old != 0 {
		t.Fatal("fixture did not exercise real history trimming")
	}
	neverStarted := submit("shared-metrics-cancel", domain.JobPriorityBackground)
	if _, err := store.CancelJob(ctx, actor, neverStarted.ID); err != nil {
		t.Fatal("cancel never-started shared metrics job")
	}
	final, body := scrapeSharedMetricsSnapshot(t, ctx, store, one, admin.Token, 4)
	if group := final.Groups[5]; group.Succeeded != 2 || group.Wait.Count != 2 || group.Duration.Count != 2 {
		t.Fatal("history trimming reduced shared counters or histograms")
	}
	if group := final.Groups[4]; group.Cancelled != 1 || group.Wait.Count != 0 || group.Duration.Count != 0 {
		t.Fatal("never-started cancellation invented timing samples")
	}
	if !final.StartedAt.Equal(initial.StartedAt) {
		t.Fatal("job execution reset the durable metrics epoch")
	}
	for _, secret := range []string{dsn, admin.Token, actor.UserID, actor.SessionID, first.ID, library.Library.ID, library.RootID, rootPath, applicationName, "shared-metrics-next-owner"} {
		if strings.Contains(body, secret) {
			t.Fatal("shared metric labels contain private source data")
		}
	}
	one.stop()
	stillRunning, _ := scrapeSharedMetricsSnapshot(t, ctx, store, two, admin.Token, 7)
	if stillRunning.Groups != final.Groups || !stillRunning.StartedAt.Equal(final.StartedAt) {
		t.Fatal("stopping another instance changed shared totals")
	}
	three := startJobMetricsRuntime(t, ctx, observer, dsn, "rebuilt", 5)
	rebuilt, _ := scrapeSharedMetricsSnapshot(t, ctx, store, three, admin.Token, 5)
	if rebuilt.Groups != final.Groups || !rebuilt.StartedAt.Equal(final.StartedAt) {
		t.Fatal("new runtime provider reset the shared database metrics")
	}
}

func TestJobMetricsRuntimePostgresAuthorizationAndBlockedSource(t *testing.T) {
	ctx, store, observer, dsn, _ := metricsIntegrationStore(t)
	admin, actor := jobMetricsRuntimeAccount(t, ctx, store)
	regularInput := domain.UserInput{Name: "SharedMetricsUser", DisplayName: "Shared metrics user", Locale: "en-US", PasswordHash: metricsIntegrationHash}
	if _, _, err := store.CreateUser(ctx, actor, regularInput, "shared-metrics-user"); err != nil {
		t.Fatal("create ordinary shared metrics user")
	}
	regular := metricsIntegrationLogin(t, ctx, store, regularInput.Name)
	otherInput := domain.UserInput{Name: "SharedMetricsOtherAdmin", DisplayName: "Other metrics admin", Locale: "en-US", PasswordHash: metricsIntegrationHash, Admin: true}
	if _, _, err := store.CreateUser(ctx, actor, otherInput, "shared-metrics-other-admin"); err != nil {
		t.Fatal("create second shared metrics administrator")
	}
	other := metricsIntegrationLogin(t, ctx, store, otherInput.Name)
	runtime := startJobMetricsRuntime(t, ctx, observer, dsn, "security", 4)
	scrapeSharedMetricsSnapshot(t, ctx, store, runtime, other.Token, 4)
	lock, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal("start owned metrics relation lock")
	}
	defer lock.Rollback(context.Background())
	// Row locks do not block MVCC reads; this relation lock isolates the DB
	// prefetch from authentication without touching account/session tables.
	if _, err := lock.Exec(ctx, `SET LOCAL lock_timeout='1s'; SET LOCAL statement_timeout='8s'; LOCK TABLE job_metric_totals IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal("hold owned metrics relation lock")
	}
	for _, tc := range []struct {
		name, token, query, host string
		status                   int
	}{
		{name: "anonymous-with-forged-admin", status: http.StatusUnauthorized},
		{name: "ordinary-user-with-forged-admin", token: regular.Token, status: http.StatusForbidden},
		{name: "query-credential", query: "?access_token=" + admin.Token, status: http.StatusUnauthorized},
		{name: "unknown-query", token: admin.Token, query: "?format=json", status: http.StatusBadRequest},
		{name: "host-with-forwarded-override", token: admin.Token, host: "evil.example", status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Reaching the locked metric source would wait for its two-second
			// prefetch limit and fail this one-second rejection budget.
			rejected, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			runtime.request(t, rejected, tc.token, tc.query, tc.host, tc.status)
		})
	}
	otherInput.Admin, otherInput.PasswordHash = false, ""
	if _, err := store.UpdateUser(ctx, actor, other.User.ID, otherInput); err != nil {
		t.Fatal("demote shared metrics administrator")
	}
	rejected, cancelRejected := context.WithTimeout(ctx, time.Second)
	runtime.request(t, rejected, other.Token, "", "", http.StatusUnauthorized)
	cancelRejected()
	other = metricsIntegrationLogin(t, ctx, store, otherInput.Name)
	rejected, cancelRejected = context.WithTimeout(ctx, time.Second)
	runtime.request(t, rejected, other.Token, "", "", http.StatusForbidden)
	cancelRejected()
	beforeFailure := time.Now()
	body := runtime.request(t, ctx, admin.Token, "", "", http.StatusServiceUnavailable)
	if body != "metrics unavailable\n" || time.Since(beforeFailure) > 4*time.Second {
		t.Fatal("blocked metric source did not return a bounded sanitized failure")
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal("release owned metrics relation lock")
	}
	scrapeSharedMetricsSnapshot(t, ctx, store, runtime, admin.Token, 4)
	writer, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal("start owned job writer lock")
	}
	defer writer.Rollback(context.Background())
	if _, err := writer.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()),17481204)`); err != nil {
		t.Fatal("hold job writer advisory lock")
	}
	readCtx, cancelRead := context.WithTimeout(ctx, time.Second)
	scrapeSharedMetricsSnapshot(t, readCtx, store, runtime, admin.Token, 4)
	cancelRead()
	if err := writer.Rollback(ctx); err != nil {
		t.Fatal("release job writer advisory lock")
	}
	if err := store.RevokeSession(ctx, actor, admin.User.ID, admin.Session.ID); err != nil {
		t.Fatal("revoke successful shared metrics session")
	}
	runtime.request(t, ctx, admin.Token, "", "", http.StatusUnauthorized)
	runtime.stop()
}
