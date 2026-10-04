package telemetry

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type jobMetricsTestSource struct {
	calls atomic.Int64
	read  func(context.Context) (app.JobMetricsSnapshot, error)
}

func (s *jobMetricsTestSource) JobMetrics(ctx context.Context) (app.JobMetricsSnapshot, error) {
	s.calls.Add(1)
	return s.read(ctx)
}

func jobMetricsTestSnapshot(scale uint64) app.JobMetricsSnapshot {
	snapshot := app.JobMetricsSnapshot{
		StartedAt: time.Unix(1700000000, 0).UTC(), ObservedAt: time.Unix(1700003600, 0).UTC(),
	}
	groups := [10][2]string{{"catalog_import", "background"}, {"catalog_import", "manual"}, {"catalog_sync", "background"}, {"catalog_sync", "manual"}, {"consistency_check", "background"}, {"consistency_check", "manual"}, {"inventory_scan", "background"}, {"inventory_scan", "manual"}, {"nfo_write", "background"}, {"nfo_write", "manual"}}
	for i, key := range groups {
		g := &snapshot.Groups[i]
		g.Kind, g.Priority = key[0], key[1]
		factor := uint64(i+1) * scale
		g.Queued, g.Running, g.ExpiredRunning = int64(factor), int64(factor*2), int64(factor*3)
		g.OldestQueuedAgeSeconds = float64(factor) * 3.75
		for _, measure := range []struct {
			h      *app.JobMetricHistogram
			bounds [12]float64
		}{{&g.Wait, app.JobWaitBoundsSeconds()}, {&g.Duration, app.JobDurationBoundsSeconds()}} {
			for bucket := range measure.h.BucketCounts {
				count := uint64(bucket+1) * factor
				value := 86401.0
				if bucket < len(measure.bounds) {
					value = measure.bounds[bucket]
				}
				measure.h.BucketCounts[bucket] = count
				measure.h.Count += count
				measure.h.SumSeconds += float64(count) * value
			}
		}
		g.Succeeded, g.Failed, g.Cancelled = int64(g.Duration.Count-3*factor), int64(factor), int64(2*factor)
	}
	return snapshot
}

func newJobMetricsTestExporter(t *testing.T, pool *testPool, source app.JobMetricsSource) *Metrics {
	t.Helper()
	m, err := NewWithJobs(pool, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error("close job metrics exporter", err)
		}
	})
	return m
}

func requestJobMetrics(m *Metrics, ctx context.Context) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil).WithContext(ctx)
	r.Header.Set("Accept", "text/plain; version=0.0.4")
	m.Handler().ServeHTTP(w, r)
	return w
}

func scrapeJobMetrics(t *testing.T, m *Metrics) (map[string]*dto.MetricFamily, string) {
	t.Helper()
	return parseJobMetrics(t, requestJobMetrics(m, context.Background()))
}

func parseJobMetrics(t *testing.T, w *httptest.ResponseRecorder) (map[string]*dto.MetricFamily, string) {
	t.Helper()
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain;") {
		t.Fatalf("job exposition status/content type: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if w.Body.Len() > 64*1024 {
		t.Fatalf("job exposition exceeds fixed 64 KiB budget: %d", w.Body.Len())
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(w.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	series := 0
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			series++
		}
	}
	if len(families) != 22 || series != 385 {
		t.Fatalf("job exposition has %d families / %d series, want 22 / 385", len(families), series)
	}
	t.Logf("job exposition: %d bytes, %d families, %d series", w.Body.Len(), len(families), series)
	return families, w.Body.String()
}

func jobMetricPoint(t *testing.T, family *dto.MetricFamily, labels map[string]string) *dto.Metric {
	t.Helper()
	if family == nil {
		t.Fatal("job metric family missing")
	}
	var result *dto.Metric
	for _, point := range family.Metric {
		actual := make(map[string]string, len(point.Label))
		for _, label := range point.Label {
			if _, duplicate := actual[label.GetName()]; duplicate {
				t.Fatal("duplicate label", label.GetName())
			}
			actual[label.GetName()] = label.GetValue()
		}
		if reflect.DeepEqual(actual, labels) {
			if result != nil {
				t.Fatal("duplicate job metric point", family.GetName(), labels)
			}
			result = point
		}
	}
	if result == nil {
		t.Fatal("missing exact fixed labels", family.GetName(), labels)
	}
	return result
}

func assertJobMetricsExposition(t *testing.T, families map[string]*dto.MetricFamily, want app.JobMetricsSnapshot) {
	t.Helper()
	legacy := map[string]dto.MetricType{
		"jelee_runtime_heap_bytes": dto.MetricType_GAUGE, "jelee_runtime_allocated_bytes_total": dto.MetricType_COUNTER,
		"jelee_runtime_gc_pause_seconds_total": dto.MetricType_COUNTER, "jelee_runtime_gc_cycles_total": dto.MetricType_COUNTER,
		"jelee_runtime_goroutines": dto.MetricType_GAUGE, "jelee_db_pool_connections_acquired": dto.MetricType_GAUGE,
		"jelee_db_pool_connections_idle": dto.MetricType_GAUGE, "jelee_db_pool_connections_constructing": dto.MetricType_GAUGE,
		"jelee_db_pool_connections_total": dto.MetricType_GAUGE, "jelee_db_pool_connections_max": dto.MetricType_GAUGE,
		"jelee_db_pool_acquire_success_total": dto.MetricType_COUNTER, "jelee_db_pool_acquire_duration_seconds_total": dto.MetricType_COUNTER,
		"jelee_db_pool_acquire_canceled_total": dto.MetricType_COUNTER, "jelee_db_pool_acquire_empty_total": dto.MetricType_COUNTER,
		"jelee_db_pool_acquire_empty_wait_seconds_total": dto.MetricType_COUNTER,
	}
	for name, typ := range legacy {
		family := families[name]
		if family == nil || family.GetType() != typ || len(family.Metric) != 1 || len(family.Metric[0].Label) != 0 {
			t.Fatal("legacy family changed", name)
		}
	}
	for _, g := range want.Groups {
		labels := map[string]string{"kind": g.Kind, "priority": g.Priority}
		for name, value := range map[string]float64{
			"jelee_jobs_shared_queued": float64(g.Queued), "jelee_jobs_shared_running": float64(g.Running),
			"jelee_jobs_shared_expired_running": float64(g.ExpiredRunning), "jelee_jobs_shared_oldest_queued_age_seconds": g.OldestQueuedAgeSeconds,
		} {
			family := families[name]
			point := jobMetricPoint(t, family, labels)
			if family.GetType() != dto.MetricType_GAUGE || len(family.Metric) != 10 || point.GetGauge().GetValue() != value {
				t.Fatal("shared gauge differs from source", name, labels)
			}
		}
		outcomes := families["jelee_jobs_shared_outcomes_total"]
		for outcome, value := range map[string]int64{"succeeded": g.Succeeded, "failed": g.Failed, "cancelled": g.Cancelled} {
			point := jobMetricPoint(t, outcomes, map[string]string{"kind": g.Kind, "priority": g.Priority, "outcome": outcome})
			if outcomes.GetType() != dto.MetricType_COUNTER || len(outcomes.Metric) != 30 || point.GetCounter().GetValue() != float64(value) {
				t.Fatal("shared outcome differs from absolute source", labels, outcome)
			}
		}
		for _, measure := range []struct {
			name   string
			want   app.JobMetricHistogram
			bounds [12]float64
		}{{"jelee_jobs_shared_initial_wait_seconds", g.Wait, app.JobWaitBoundsSeconds()}, {"jelee_jobs_shared_duration_seconds", g.Duration, app.JobDurationBoundsSeconds()}} {
			family := families[measure.name]
			point := jobMetricPoint(t, family, labels)
			hist := point.GetHistogram()
			if family.GetType() != dto.MetricType_HISTOGRAM || len(family.Metric) != 10 || hist == nil || len(hist.Bucket) != 13 {
				t.Fatal("shared histogram shape changed", measure.name, labels)
			}
			if hist.GetSampleCount() != measure.want.Count || hist.GetSampleSum() != measure.want.SumSeconds {
				t.Fatal("shared histogram count/sum differs from absolute source", measure.name, labels)
			}
			var cumulative uint64
			for i, bucket := range hist.Bucket {
				bound := math.Inf(1)
				if i < len(measure.bounds) {
					bound = measure.bounds[i]
				}
				cumulative += measure.want.BucketCounts[i]
				if bucket.GetUpperBound() != bound || bucket.GetCumulativeCount() != cumulative || bucket.Exemplar != nil {
					t.Fatal("shared histogram must expose fixed cumulative buckets without exemplars", measure.name, labels, i)
				}
			}
			if cumulative != hist.GetSampleCount() {
				t.Fatal("+Inf bucket does not equal count", measure.name, labels)
			}
		}
	}
}

func TestJobExporterRejectsMissingSources(t *testing.T) {
	for _, tc := range []struct {
		name string
		pool app.PoolStatsSource
		jobs app.JobMetricsSource
	}{{"jobs", &testPool{}, nil}, {"pool", nil, &jobMetricsTestSource{}}} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewWithJobs(tc.pool, tc.jobs)
			if m != nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = m.Shutdown(ctx)
			}
			if err == nil {
				t.Fatal("missing source accepted")
			}
		})
	}
}

func TestJobExporterFixedContractAbsoluteSnapshotsAndPrivacy(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "secret.fixture=job-dsn-secret,arbitrary_path=/job-private-fixture,service.name=secret-job-resource")
	t.Setenv("OTEL_SERVICE_NAME", "secret-job-service")
	current := jobMetricsTestSnapshot(1)
	source := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) { return current, nil }}
	pool := &testPool{value: samplePool()}
	m := newJobMetricsTestExporter(t, pool, source)
	initial := current
	for round := 0; round < 7; round++ {
		switch round {
		case 2:
			g := &current.Groups[0]
			g.Queued, g.Running, g.ExpiredRunning, g.OldestQueuedAgeSeconds = 0, 0, 0, 0
			g.Succeeded++
			g.Wait.Count++
			g.Wait.SumSeconds += 0.25
			g.Wait.BucketCounts[1]++
			g.Duration.Count++
			g.Duration.SumSeconds += 2
			g.Duration.BucketCounts[1]++
			current.ObservedAt = current.ObservedAt.Add(time.Second)
		case 3:
			// A restored DB may return lower cumulative values; do not hide resets.
			current = initial
		case 4:
			current = jobMetricsTestSnapshot(0)
		case 5:
			current = jobMetricsTestSnapshot(1 << 40)
		case 6:
			// Zero-second samples are valid, including a clamped DB clock rollback.
			current = jobMetricsTestSnapshot(0)
			current.Groups[0].Succeeded = 1
			current.Groups[0].Wait.Count, current.Groups[0].Wait.BucketCounts[0] = 1, 1
			current.Groups[0].Duration.Count, current.Groups[0].Duration.BucketCounts[0] = 1, 1
		}
		families, body := scrapeJobMetrics(t, m)
		assertJobMetricsExposition(t, families, current)
		for _, forbidden := range []string{"secret", "arbitrary_path", "/job-private-fixture", "target_info", "otel_scope", "service_name", "_created", "go_memstats", "process_"} {
			if strings.Contains(body, forbidden) {
				t.Error("unexpected resource or global collector output", forbidden)
			}
		}
		if source.calls.Load() != int64(round+1) || pool.calls.Load() != int64(round+1) {
			t.Fatal("one request must read each source exactly once")
		}
	}
}

func TestJobExporterInstancesShareSourceAndKeepLocalPools(t *testing.T) {
	current := jobMetricsTestSnapshot(1)
	source := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) { return current, nil }}
	poolOne, poolTwo := &testPool{value: samplePool()}, &testPool{value: samplePool()}
	poolTwo.value.MaxConns = 31
	one := newJobMetricsTestExporter(t, poolOne, source)
	two := newJobMetricsTestExporter(t, poolTwo, source)
	for _, tc := range []struct {
		m   *Metrics
		max float64
	}{{one, 8}, {two, 31}, {one, 8}} {
		families, _ := scrapeJobMetrics(t, tc.m)
		assertJobMetricsExposition(t, families, current)
		if families["jelee_db_pool_connections_max"].Metric[0].GetGauge().GetValue() != tc.max {
			t.Fatal("job source sharing contaminated a local pool")
		}
	}
	if err := one.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertJobMetricsUnavailable(t, requestJobMetrics(one, context.Background()))
	current = jobMetricsTestSnapshot(2)
	families, _ := scrapeJobMetrics(t, two)
	assertJobMetricsExposition(t, families, current)
	// Reconstructing a provider must preserve the source's cumulative totals.
	three := newJobMetricsTestExporter(t, &testPool{value: samplePool()}, source)
	families, _ = scrapeJobMetrics(t, three)
	assertJobMetricsExposition(t, families, current)
	if source.calls.Load() != 5 || poolOne.calls.Load() != 2 || poolTwo.calls.Load() != 2 {
		t.Fatal("instance shutdown or reconstruction accessed the wrong source")
	}
}

func TestJobExporterConcurrentInstancesKeepRequestFrames(t *testing.T) {
	initial, later := jobMetricsTestSnapshot(1), jobMetricsTestSnapshot(2)
	var reads atomic.Int64
	source := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) {
		if reads.Add(1) == 1 {
			return initial, nil
		}
		return later, nil
	}}
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	poolOne := &testPool{value: samplePool(), read: func() {
		once.Do(func() { close(entered); <-release })
	}}
	one := newJobMetricsTestExporter(t, poolOne, source)
	two := newJobMetricsTestExporter(t, &testPool{value: samplePool()}, source)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- requestJobMetrics(one, context.Background()) }()
	awaitJobMetricsSignal(t, entered, "first instance did not reach local collection after prefetch")
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { second <- requestJobMetrics(two, context.Background()) }()
	families, _ := parseJobMetrics(t, awaitJobMetricsResponse(t, second))
	assertJobMetricsExposition(t, families, later)
	releaseOnce.Do(func() { close(release) })
	families, _ = parseJobMetrics(t, awaitJobMetricsResponse(t, first))
	assertJobMetricsExposition(t, families, initial)
	if source.calls.Load() != 2 {
		t.Fatal("concurrent instances reread or shared a prepared DB snapshot")
	}
}

func assertJobMetricsUnavailable(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != "metrics unavailable\n" {
		t.Fatalf("unsafe or partial failed job exposition: status=%d body=%q", w.Code, w.Body.String())
	}
	for _, values := range w.Header() {
		for _, value := range values {
			if strings.Contains(value, "secret") || strings.Contains(value, "jelee_jobs") {
				t.Fatal("failed job exposition leaked a source detail in headers")
			}
		}
	}
}

func TestJobExporterFailureRejectsStaleAndPartialMetrics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*app.JobMetricsSnapshot)
		err    error
	}{
		{name: "source-error", err: errors.New("secret DSN=postgres://private/path SELECT * FROM jobs")},
		{name: "source-cancelled", err: context.Canceled},
		{name: "source-deadline", err: context.DeadlineExceeded},
		{name: "zero-epoch", mutate: func(s *app.JobMetricsSnapshot) { s.StartedAt = time.Time{} }},
		{name: "zero-observation", mutate: func(s *app.JobMetricsSnapshot) { s.ObservedAt = time.Time{} }},
		{name: "unknown-kind", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[0].Kind = "secret-library-path" }},
		{name: "unknown-priority", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[0].Priority = "secret-owner" }},
		{name: "duplicate-group", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[1] = s.Groups[0] }},
		{name: "negative-queued", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[0].Queued = -1 }},
		{name: "negative-running", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[1].Running = -1 }},
		{name: "negative-expired", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[2].ExpiredRunning = -1 }},
		{name: "negative-succeeded", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[0].Succeeded = -1 }},
		{name: "negative-failed", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[1].Failed = -1 }},
		{name: "negative-cancelled", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[2].Cancelled = -1 }},
		{name: "negative-age", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[0].OldestQueuedAgeSeconds = -1 }},
		{name: "nan-age", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[1].OldestQueuedAgeSeconds = math.NaN() }},
		{name: "infinite-age", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[2].OldestQueuedAgeSeconds = math.Inf(1) }},
		{name: "age-without-queued-job", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[0].Queued = 0 }},
		{name: "wait-count-mismatch", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[0].Wait.Count++ }},
		{name: "duration-count-mismatch", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[1].Duration.BucketCounts[0]++ }},
		{name: "bucket-overflow", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[2].Wait.BucketCounts[0] = math.MaxUint64 }},
		{name: "wait-negative-sum", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[0].Wait.SumSeconds = -1 }},
		{name: "wait-nan-sum", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[1].Wait.SumSeconds = math.NaN() }},
		{name: "duration-infinite-sum", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[2].Duration.SumSeconds = math.Inf(1) }},
		{name: "sum-exceeds-storage-range", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[3].Wait.SumSeconds = 1e25 }},
		{name: "count-exceeds-storage-range", mutate: func(s *app.JobMetricsSnapshot) {
			s.Groups[0].Wait = app.JobMetricHistogram{Count: uint64(math.MaxInt64) + 1, BucketCounts: [13]uint64{uint64(math.MaxInt64) + 1}}
		}},
		{name: "duration-exceeds-terminal-count", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[1].Succeeded-- }},
		{name: "empty-histogram-nonzero-sum", mutate: func(s *app.JobMetricsSnapshot) { s.Groups[3].Duration = app.JobMetricHistogram{SumSeconds: 1} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			valid := jobMetricsTestSnapshot(1)
			current, failure := valid, error(nil)
			source := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) { return current, failure }}
			pool := &testPool{value: samplePool()}
			m := newJobMetricsTestExporter(t, pool, source)
			scrapeJobMetrics(t, m)
			if tc.mutate != nil {
				tc.mutate(&current)
			}
			failure = tc.err
			assertJobMetricsUnavailable(t, requestJobMetrics(m, context.Background()))
			if source.calls.Load() != 2 || pool.calls.Load() != 1 {
				t.Fatal("failed prefetch accessed local collection or skipped the source")
			}
			current, failure = valid, nil
			families, _ := scrapeJobMetrics(t, m)
			assertJobMetricsExposition(t, families, valid)
			if source.calls.Load() != 3 || pool.calls.Load() != 2 {
				t.Fatal("successful retry did not read a fresh snapshot")
			}
		})
	}
}

func TestJobExporterPrefetchUsesRequestContextAndTwoSecondLimit(t *testing.T) {
	for _, early := range []bool{false, true} {
		name := "two-second-limit"
		if early {
			name = "earlier-request-deadline"
		}
		t.Run(name, func(t *testing.T) {
			var deadline, entered time.Time
			var hasDeadline bool
			source := &jobMetricsTestSource{read: func(ctx context.Context) (app.JobMetricsSnapshot, error) {
				entered = time.Now()
				deadline, hasDeadline = ctx.Deadline()
				return jobMetricsTestSnapshot(1), nil
			}}
			m := newJobMetricsTestExporter(t, &testPool{value: samplePool()}, source)
			ctx := context.Background()
			var expected time.Time
			if early {
				expected = time.Now().Add(time.Second)
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, expected)
				defer cancel()
			}
			before := time.Now()
			w := requestJobMetrics(m, ctx)
			if w.Code != http.StatusOK || !hasDeadline {
				t.Fatal("prefetch lacks a bounded context", w.Code)
			}
			if early {
				if !deadline.Equal(expected) {
					t.Fatal("prefetch extended the request deadline")
				}
			} else if deadline.Before(before.Add(2*time.Second)) || deadline.After(entered.Add(2*time.Second)) {
				t.Fatal("prefetch deadline is not two seconds from admission")
			}
		})
	}
	for _, during := range []bool{false, true} {
		name := "already-cancelled"
		if during {
			name = "cancelled-source-returned-success"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) {
				cancel()
				return jobMetricsTestSnapshot(1), nil
			}}
			pool := &testPool{value: samplePool()}
			m := newJobMetricsTestExporter(t, pool, source)
			if !during {
				cancel()
			}
			assertJobMetricsUnavailable(t, requestJobMetrics(m, ctx))
			wantCalls := int64(0)
			if during {
				wantCalls = 1
			}
			if source.calls.Load() != wantCalls || pool.calls.Load() != 0 {
				t.Fatal("cancelled request reached an unexpected source")
			}
		})
	}
}

func awaitJobMetricsSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func TestJobExporterRejectsSuccessfulSourceAfterPrefetchDeadline(t *testing.T) {
	source := &jobMetricsTestSource{read: func(ctx context.Context) (app.JobMetricsSnapshot, error) {
		<-ctx.Done()
		return jobMetricsTestSnapshot(1), nil
	}}
	pool := &testPool{value: samplePool()}
	m := newJobMetricsTestExporter(t, pool, source)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	assertJobMetricsUnavailable(t, requestJobMetrics(m, ctx))
	if ctx.Err() != nil || source.calls.Load() != 1 || pool.calls.Load() != 0 {
		t.Fatal("expired child prefetch entered collection or consumed the parent deadline")
	}
}

func TestJobExporterCancellationDuringCollectionRejectsPartialBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) {
		return jobMetricsTestSnapshot(1), nil
	}}
	pool := &testPool{value: samplePool(), read: cancel}
	m := newJobMetricsTestExporter(t, pool, source)
	w := requestJobMetrics(m, ctx)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "metrics unavailable") ||
		strings.Contains(w.Body.String(), "jelee_") || source.calls.Load() != 1 || pool.calls.Load() != 1 {
		t.Fatalf("canceled collection exposed a partial body: status=%d body=%q", w.Code, w.Body.String())
	}
	pool.read = nil
	families, _ := scrapeJobMetrics(t, m)
	assertJobMetricsExposition(t, families, jobMetricsTestSnapshot(1))
}

func awaitJobMetricsResponse(t *testing.T, responses <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-responses:
		return w
	case <-time.After(time.Second):
		t.Fatal("job metrics request did not finish")
		return nil
	}
}

func TestJobExporterWaitingPrefetchHonorsCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			source := &jobMetricsTestSource{read: func(ctx context.Context) (app.JobMetricsSnapshot, error) {
				once.Do(func() {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
				})
				return jobMetricsTestSnapshot(1), ctx.Err()
			}}
			pool := &testPool{value: samplePool()}
			m := newJobMetricsTestExporter(t, pool, source)
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			first := make(chan *httptest.ResponseRecorder, 1)
			go func() { first <- requestJobMetrics(m, context.Background()) }()
			awaitJobMetricsSignal(t, entered, "first scrape did not reach DB prefetch")
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			observed := &observedContext{Context: ctx, checked: make(chan struct{})}
			second := make(chan *httptest.ResponseRecorder, 1)
			go func() { second <- requestJobMetrics(m, observed) }()
			awaitJobMetricsSignal(t, observed.checked, "waiting scrape did not observe cancellation")
			if !deadline {
				cancel()
			}
			assertJobMetricsUnavailable(t, awaitJobMetricsResponse(t, second))
			if source.calls.Load() != 1 || pool.calls.Load() != 0 {
				t.Fatal("waiting cancelled scrape accessed DB or local collectors")
			}
			releaseOnce.Do(func() { close(release) })
			if w := awaitJobMetricsResponse(t, first); w.Code != http.StatusOK {
				t.Fatal("first scrape failed after releasing prefetch", w.Code)
			}
			scrapeJobMetrics(t, m)
			if source.calls.Load() != 2 || pool.calls.Load() != 2 {
				t.Fatal("scrape gate did not recover with one fresh read")
			}
		})
	}
}

func TestJobExporterShutdownWaitsForContextAwarePrefetchAndCanRetry(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	source := &jobMetricsTestSource{read: func(ctx context.Context) (app.JobMetricsSnapshot, error) {
		close(entered)
		defer close(exited)
		<-ctx.Done()
		return app.JobMetricsSnapshot{}, ctx.Err()
	}}
	pool := &testPool{value: samplePool()}
	m := newJobMetricsTestExporter(t, pool, source)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	t.Cleanup(cancelRequest)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- requestJobMetrics(m, requestCtx) }()
	awaitJobMetricsSignal(t, entered, "shutdown fixture did not reach DB prefetch")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := m.Shutdown(shutdownCtx)
	cancelShutdown()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown returned before active DB read exited", err)
	}
	select {
	case <-exited:
		t.Fatal("fixture DB read exited before request cancellation")
	default:
	}
	assertJobMetricsUnavailable(t, requestJobMetrics(m, context.Background()))
	if source.calls.Load() != 1 || pool.calls.Load() != 0 {
		t.Fatal("shutdown admitted collection while DB read was active")
	}
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	retryObserved := &observedContext{Context: retryCtx, checked: make(chan struct{})}
	retried := make(chan error, 1)
	go func() { retried <- m.Shutdown(retryObserved) }()
	awaitJobMetricsSignal(t, retryObserved.checked, "shutdown retry did not wait with its context")
	cancelRequest()
	awaitJobMetricsSignal(t, exited, "prefetch ignored request cancellation")
	assertJobMetricsUnavailable(t, awaitJobMetricsResponse(t, first))
	select {
	case err := <-retried:
		if err != nil {
			t.Fatal("shutdown retry did not complete after DB exit", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown retry stayed blocked after DB read exited")
	}
	assertJobMetricsUnavailable(t, requestJobMetrics(m, context.Background()))
	if source.calls.Load() != 1 || pool.calls.Load() != 0 {
		t.Fatal("shutdown or cancelled prefetch reached a collector")
	}
}
