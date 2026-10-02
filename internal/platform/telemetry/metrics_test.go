package telemetry

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type testPool struct {
	value app.PoolStatsSnapshot
	calls atomic.Int64
	read  func()
}

func (p *testPool) PoolStats() app.PoolStatsSnapshot {
	p.calls.Add(1)
	if p.read != nil {
		p.read()
	}
	return p.value
}

func samplePool() app.PoolStatsSnapshot {
	return app.PoolStatsSnapshot{
		AcquiredConns: 2, IdleConns: 3, ConstructingConns: 1, TotalConns: 6, MaxConns: 8,
		AcquireCount: 17, AcquireDuration: 2500 * time.Millisecond,
		CanceledAcquireCount: 4, EmptyAcquireCount: 5, EmptyAcquireWaitTime: 1250 * time.Millisecond,
	}
}

func sampleRuntime() runtimeSnapshot {
	return runtimeSnapshot{heapBytes: 1024, allocatedBytes: 8192, gcPauseNS: 750000000, gcCycles: 7, goroutines: 11}
}

func testMetrics(t *testing.T, pool *testPool, read func() runtimeSnapshot, readers ...sdkmetric.Reader) *Metrics {
	t.Helper()
	m, err := newMetrics(pool, read, readers...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return m
}

type observed struct {
	value   float64
	counter bool
}

func collectSDK(t *testing.T, reader *sdkmetric.ManualReader) map[string]observed {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if name, ok := rm.Resource.Set().Value(attribute.Key("service.name")); !ok || name.AsString() != "jelee" {
		t.Fatal("fixed internal service.name missing")
	}
	result := make(map[string]observed)
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			var got observed
			switch data := m.Data.(type) {
			case metricdata.Gauge[int64]:
				got.value = pointValue(t, data.DataPoints)
			case metricdata.Gauge[float64]:
				got.value = pointValue(t, data.DataPoints)
			case metricdata.Sum[int64]:
				if !data.IsMonotonic || data.Temporality != metricdata.CumulativeTemporality {
					t.Fatalf("%s is not a cumulative monotonic sum", m.Name)
				}
				got = observed{pointValue(t, data.DataPoints), true}
			case metricdata.Sum[float64]:
				if !data.IsMonotonic || data.Temporality != metricdata.CumulativeTemporality {
					t.Fatalf("%s is not a cumulative monotonic sum", m.Name)
				}
				got = observed{pointValue(t, data.DataPoints), true}
			default:
				t.Fatalf("unexpected aggregation %T", m.Data)
			}
			if _, exists := result[m.Name]; exists {
				t.Fatalf("duplicate instrument %s", m.Name)
			}
			result[m.Name] = got
		}
	}
	return result
}

func pointValue[N int64 | float64](t *testing.T, points []metricdata.DataPoint[N]) float64 {
	t.Helper()
	if len(points) != 1 || points[0].Attributes.Len() != 0 {
		t.Fatalf("expected one point without attributes, got %v", points)
	}
	return float64(points[0].Value)
}

func TestObservableSnapshotsAreAbsolute(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	pool := &testPool{value: samplePool()}
	runtimeValue := sampleRuntime()
	var runtimeCalls atomic.Int64
	testMetrics(t, pool, func() runtimeSnapshot { runtimeCalls.Add(1); return runtimeValue }, reader)
	want := map[string]observed{
		"jelee.runtime.heap": {1024, false}, "jelee.runtime.allocated": {8192, true},
		"jelee.runtime.gc.pause": {0.75, true}, "jelee.runtime.gc.cycles": {7, true},
		"jelee.runtime.goroutines":           {11, false},
		"jelee.db.pool.connections.acquired": {2, false}, "jelee.db.pool.connections.idle": {3, false},
		"jelee.db.pool.connections.constructing": {1, false}, "jelee.db.pool.connections.total": {6, false},
		"jelee.db.pool.connections.max": {8, false}, "jelee.db.pool.acquire.success": {17, true},
		"jelee.db.pool.acquire.duration": {2.5, true}, "jelee.db.pool.acquire.canceled": {4, true},
		"jelee.db.pool.acquire.empty": {5, true}, "jelee.db.pool.acquire.empty.wait": {1.25, true},
	}
	for round := range 3 {
		if round == 2 {
			pool.value.AcquiredConns, pool.value.IdleConns = 1, 4
			pool.value.AcquireCount = 20
			runtimeValue.heapBytes, runtimeValue.allocatedBytes = 512, 9000
			want["jelee.db.pool.connections.acquired"] = observed{1, false}
			want["jelee.db.pool.connections.idle"] = observed{4, false}
			want["jelee.db.pool.acquire.success"] = observed{20, true}
			want["jelee.runtime.heap"] = observed{512, false}
			want["jelee.runtime.allocated"] = observed{9000, true}
		}
		got := collectSDK(t, reader)
		if len(got) != len(want) {
			t.Fatalf("round %d: %d instruments, want %d", round, len(got), len(want))
		}
		for name, expected := range want {
			if actual, ok := got[name]; !ok || actual != expected {
				t.Errorf("round %d %s = %v, want %v", round, name, actual, expected)
			}
		}
	}
	if pool.calls.Load() != 3 || runtimeCalls.Load() != 3 {
		t.Fatal("collection must read each source exactly once")
	}
}

func scrape(t *testing.T, m *Metrics) (map[string]*dto.MetricFamily, string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "text/plain; version=0.0.4")
	m.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain;") {
		t.Fatalf("exposition status/content type: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if w.Body.Len() > 12*1024 {
		t.Fatalf("exposition exceeds fixed 12 KiB budget: %d", w.Body.Len())
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(w.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	return families, w.Body.String()
}

func TestExporterNamesTypesAndResourcePrivacy(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "secret.fixture=dsn-secret-fixture,arbitrary_path=/private-fixture,service.name=secret-env-service")
	t.Setenv("OTEL_SERVICE_NAME", "secret-service-fixture")
	pool := &testPool{value: samplePool()}
	m := testMetrics(t, pool, sampleRuntime)
	want := map[string]observed{
		"jelee_runtime_heap_bytes": {1024, false}, "jelee_runtime_allocated_bytes_total": {8192, true},
		"jelee_runtime_gc_pause_seconds_total": {0.75, true}, "jelee_runtime_gc_cycles_total": {7, true},
		"jelee_runtime_goroutines":           {11, false},
		"jelee_db_pool_connections_acquired": {2, false}, "jelee_db_pool_connections_idle": {3, false},
		"jelee_db_pool_connections_constructing": {1, false}, "jelee_db_pool_connections_total": {6, false},
		"jelee_db_pool_connections_max": {8, false}, "jelee_db_pool_acquire_success_total": {17, true},
		"jelee_db_pool_acquire_duration_seconds_total": {2.5, true}, "jelee_db_pool_acquire_canceled_total": {4, true},
		"jelee_db_pool_acquire_empty_total": {5, true}, "jelee_db_pool_acquire_empty_wait_seconds_total": {1.25, true},
	}
	for range 2 {
		families, body := scrape(t, m)
		if len(families) != len(want) {
			t.Fatalf("exposed %d families, want exactly %d: %s", len(families), len(want), body)
		}
		for name, expected := range want {
			family := families[name]
			if family == nil || len(family.Metric) != 1 || len(family.Metric[0].Label) != 0 {
				t.Fatalf("%s must have one unlabeled series", name)
			}
			typ, value := dto.MetricType_GAUGE, family.Metric[0].GetGauge().GetValue()
			if expected.counter {
				typ, value = dto.MetricType_COUNTER, family.Metric[0].GetCounter().GetValue()
			}
			if family.GetType() != typ || value != expected.value {
				t.Errorf("%s = %v %v, want %v %v", name, family.GetType(), value, typ, expected.value)
			}
		}
		for _, forbidden := range []string{"secret", "arbitrary_path", "/private-fixture", "target_info", "otel_scope", "service_name", "go_memstats", "process_"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("unexpected resource/global collector output %q", forbidden)
			}
		}
	}
	if pool.calls.Load() != 2 {
		t.Fatal("scrapes must read one snapshot each")
	}
}

func TestIndependentRegistriesAndRealRuntime(t *testing.T) {
	one := testMetrics(t, &testPool{value: samplePool()}, sampleRuntime)
	p := samplePool()
	p.MaxConns = 31
	two := testMetrics(t, &testPool{value: p}, sampleRuntime)
	for _, tc := range []struct {
		m   *Metrics
		max float64
	}{{one, 8}, {two, 31}, {one, 8}} {
		families, _ := scrape(t, tc.m)
		if got := families["jelee_db_pool_connections_max"].Metric[0].GetGauge().GetValue(); got != tc.max {
			t.Fatalf("private registry max = %v, want %v", got, tc.max)
		}
	}
	actual, err := New(&testPool{value: samplePool()})
	if err != nil {
		t.Fatal(err)
	}
	defer actual.Shutdown(context.Background())
	families, _ := scrape(t, actual)
	heap := families["jelee_runtime_heap_bytes"].Metric[0].GetGauge().GetValue()
	allocated := families["jelee_runtime_allocated_bytes_total"].Metric[0].GetCounter().GetValue()
	if heap <= 0 || allocated < heap || families["jelee_runtime_goroutines"].Metric[0].GetGauge().GetValue() <= 0 {
		t.Fatal("real runtime snapshot is invalid")
	}
	for name, family := range families {
		for _, point := range family.Metric {
			value := point.GetGauge().GetValue() + point.GetCounter().GetValue()
			if math.IsInf(value, 0) || math.IsNaN(value) || value < 0 {
				t.Fatalf("%s must be finite and nonnegative", name)
			}
		}
	}
}

func TestShutdownWaitsForSnapshotAndCanRetry(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	pool := &testPool{value: samplePool(), read: func() { close(entered); <-release }}
	reader := sdkmetric.NewManualReader()
	m := testMetrics(t, pool, sampleRuntime, reader)
	done := make(chan error, 1)
	go func() {
		var rm metricdata.ResourceMetrics
		done <- reader.Collect(context.Background(), &rm)
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := m.Shutdown(ctx)
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown of active snapshot = %v", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err = reader.Collect(context.Background(), &rm); !errors.Is(err, sdkmetric.ErrReaderShutdown) {
		t.Fatalf("reader still collects after shutdown: %v", err)
	}
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusServiceUnavailable || pool.calls.Load() != 1 {
		t.Fatal("shutdown permits later pool reads")
	}
}

func TestConcurrentReadersScrapesAndShutdown(t *testing.T) {
	first, second := sdkmetric.NewManualReader(), sdkmetric.NewManualReader()
	pool := &testPool{value: samplePool()}
	m := testMetrics(t, pool, sampleRuntime, first, second)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 18 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 20 {
				if i%3 == 0 {
					w := httptest.NewRecorder()
					m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
					if w.Code != http.StatusOK && w.Code != http.StatusServiceUnavailable {
						t.Errorf("concurrent scrape status %d", w.Code)
					}
				} else {
					reader := first
					if i%3 == 2 {
						reader = second
					}
					var rm metricdata.ResourceMetrics
					if err := reader.Collect(context.Background(), &rm); err != nil && !errors.Is(err, sdkmetric.ErrReaderShutdown) {
						t.Errorf("concurrent collect: %v", err)
					}
				}
			}
		}()
	}
	close(start)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	after := pool.calls.Load()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if pool.calls.Load() != after || w.Code != http.StatusServiceUnavailable {
		t.Fatal("post-shutdown scrape reads a source")
	}
}

// This reader exercises the SDK boundary deterministically: shutdown performs
// a real collection while holding the SDK pipeline lock. Our stopped callback
// must remain callable rather than waiting on a gate held by Shutdown.
type collectOnShutdownReader struct{ *sdkmetric.ManualReader }

func (r *collectOnShutdownReader) Shutdown(ctx context.Context) error {
	var rm metricdata.ResourceMetrics
	return errors.Join(r.Collect(ctx, &rm), r.ManualReader.Shutdown(ctx))
}

func TestShutdownAllowsSDKCollectionToFinish(t *testing.T) {
	reader := &collectOnShutdownReader{sdkmetric.NewManualReader()}
	pool := &testPool{value: samplePool()}
	m := testMetrics(t, pool, sampleRuntime, reader)
	collectSDK(t, reader.ManualReader)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("SDK collection could not finish during shutdown: %v", err)
	}
	if pool.calls.Load() != 1 {
		t.Fatal("shutdown collection accessed the pool after stop")
	}
}

func TestNewRejectsMissingSource(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("nil pool accepted")
	}
}

type observedContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (ctx *observedContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.checked) })
	return ctx.Context.Done()
}

func TestWaitingExporterScrapeHonorsCancellation(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "cancel"
		if timeout {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var firstRead, releaseOnce sync.Once
			pool := &testPool{value: samplePool(), read: func() {
				firstRead.Do(func() { close(entered); <-release })
			}}
			m := testMetrics(t, pool, sampleRuntime)
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			first := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				first <- w
			}()
			<-entered
			ctx, cancel := context.WithCancel(context.Background())
			if timeout {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			observedCtx := &observedContext{Context: ctx, checked: make(chan struct{})}
			second := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/metrics", nil).WithContext(observedCtx)
				m.Handler().ServeHTTP(w, r)
				second <- w
			}()
			select {
			case <-observedCtx.checked:
			case <-time.After(time.Second):
				t.Fatal("waiting exporter scrape never checked request cancellation")
			}
			if !timeout {
				cancel()
			}
			select {
			case w := <-second:
				if w.Code != http.StatusServiceUnavailable {
					t.Fatalf("waiting canceled scrape status = %d", w.Code)
				}
			case <-time.After(time.Second):
				t.Fatal("waiting scrape ignored request cancellation")
			}
			if pool.calls.Load() != 1 {
				t.Fatal("waiting scrape entered source collection")
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case w := <-first:
				if w.Code != http.StatusOK {
					t.Fatalf("first scrape status = %d", w.Code)
				}
			case <-time.After(time.Second):
				t.Fatal("first exporter scrape failed to finish after snapshot release")
			}
			scrape(t, m)
			if pool.calls.Load() != 2 {
				t.Fatal("scrape gate did not recover after cancellation")
			}
		})
	}
}

func TestShutdownRejectsNewScrapesBeforeSnapshotFinishes(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	pool := &testPool{value: samplePool(), read: func() { close(entered); <-release }}
	m := testMetrics(t, pool, sampleRuntime)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	first := make(chan struct{})
	go func() {
		m.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
		close(first)
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stopping := make(chan error, 1)
	go func() { stopping <- m.Shutdown(ctx) }()
	<-m.stopScrapes
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		second <- w
	}()
	select {
	case w := <-second:
		if w.Code != http.StatusServiceUnavailable || pool.calls.Load() != 1 {
			t.Fatal("shutdown admitted a new exporter scrape")
		}
	case <-time.After(time.Second):
		t.Fatal("new scrape waited for a snapshot after shutdown started")
	}
	if err := <-stopping; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active snapshot shutdown = %v", err)
	}
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("timed out shutdown reopened scrape admission")
	}
	releaseOnce.Do(func() { close(release) })
	<-first
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pool.calls.Load() != 1 {
		t.Fatal("rejected scrapes accessed the source")
	}
}
