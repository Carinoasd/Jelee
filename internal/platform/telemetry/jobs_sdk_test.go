package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/otel/attribute"
	otelexport "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func jobSDKFrame(p *jobMetricsProducer, snapshot app.JobMetricsSnapshot, ctx context.Context) *jobMetricCollection {
	c := &jobMetricCollection{snapshot: snapshot, requestContext: ctx}
	p.active.Store(c)
	return c
}

func collectJobSDK(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var result metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &result); err != nil {
		t.Fatal("real SDK collection:", err)
	}
	metrics := map[string]metricdata.Metrics{}
	for _, scope := range result.ScopeMetrics {
		for _, m := range scope.Metrics {
			if strings.HasPrefix(m.Name, "jelee.jobs.shared.") {
				if _, duplicate := metrics[m.Name]; duplicate {
					t.Fatal("duplicate SDK job metric", m.Name)
				}
				metrics[m.Name] = m
			}
		}
	}
	if len(metrics) != 7 {
		t.Fatalf("SDK returned %d job metrics, want 7", len(metrics))
	}
	return metrics
}

func assertJobSDK(t *testing.T, got map[string]metricdata.Metrics, want app.JobMetricsSnapshot) {
	t.Helper()
	queued, ok := got["jelee.jobs.shared.queued"].Data.(metricdata.Gauge[int64])
	if !ok || len(queued.DataPoints) != 4 {
		t.Fatal("SDK queued gauge lost its four groups")
	}
	oldest, ok := got["jelee.jobs.shared.oldest_queued_age"].Data.(metricdata.Gauge[float64])
	if !ok || len(oldest.DataPoints) != 4 {
		t.Fatal("SDK age gauge lost its four groups")
	}
	for i, group := range want.Groups {
		if queued.DataPoints[i].Value != group.Queued || oldest.DataPoints[i].Value != group.OldestQueuedAgeSeconds ||
			!queued.DataPoints[i].Time.Equal(want.ObservedAt) || !oldest.DataPoints[i].Time.Equal(want.ObservedAt) {
			t.Fatal("SDK gauges changed the source value or observation time")
		}
	}
	sum, ok := got["jelee.jobs.shared.outcomes"].Data.(metricdata.Sum[int64])
	if !ok || !sum.IsMonotonic || sum.Temporality != metricdata.CumulativeTemporality || len(sum.DataPoints) != 12 {
		t.Fatal("outcomes must reach the SDK as a cumulative monotonic sum")
	}
	for _, point := range sum.DataPoints {
		if !point.StartTime.Equal(want.StartedAt) || !point.Time.Equal(want.ObservedAt) {
			t.Fatal("counter lost database epoch or observation time")
		}
		kind, _ := point.Attributes.Value(attribute.Key("kind"))
		priority, _ := point.Attributes.Value(attribute.Key("priority"))
		outcome, _ := point.Attributes.Value(attribute.Key("outcome"))
		found := false
		for _, group := range want.Groups {
			if group.Kind != kind.AsString() || group.Priority != priority.AsString() {
				continue
			}
			value, valid := map[string]int64{"succeeded": group.Succeeded, "failed": group.Failed, "cancelled": group.Cancelled}[outcome.AsString()]
			if !valid || point.Value != value {
				t.Fatal("SDK counter changed the source's absolute outcome")
			}
			found = true
		}
		if !found {
			t.Fatal("SDK counter has unknown dimensions")
		}
	}
	for _, measure := range []struct {
		name   string
		bounds [12]float64
		value  func(app.JobMetricGroup) app.JobMetricHistogram
	}{
		{"jelee.jobs.shared.initial_wait", app.JobWaitBoundsSeconds(), func(g app.JobMetricGroup) app.JobMetricHistogram { return g.Wait }},
		{"jelee.jobs.shared.duration", app.JobDurationBoundsSeconds(), func(g app.JobMetricGroup) app.JobMetricHistogram { return g.Duration }},
	} {
		m := got[measure.name]
		hist, ok := m.Data.(metricdata.Histogram[float64])
		if !ok || m.Unit != "s" || hist.Temporality != metricdata.CumulativeTemporality || len(hist.DataPoints) != 4 {
			t.Fatal("SDK histogram type, units or temporality changed", measure.name)
		}
		for i, point := range hist.DataPoints {
			expected := measure.value(want.Groups[i])
			if !point.StartTime.Equal(want.StartedAt) || !point.Time.Equal(want.ObservedAt) ||
				point.Count != expected.Count || point.Sum != expected.SumSeconds ||
				!reflect.DeepEqual(point.Bounds, measure.bounds[:]) ||
				!reflect.DeepEqual(point.BucketCounts, expected.BucketCounts[:]) {
				t.Fatal("SDK must retain the database's absolute, noncumulative histogram buckets", measure.name, i)
			}
		}
	}
}

func TestJobSDKCumulativeEpochAndOwnedCollectionResults(t *testing.T) {
	p := &jobMetricsProducer{}
	reader := sdkmetric.NewManualReader(sdkmetric.WithProducer(p))
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	want := jobMetricsTestSnapshot(1)
	frame := jobSDKFrame(p, want, context.Background())
	frame.gatherStarted.Store(true)
	first := collectJobSDK(t, reader)
	second := collectJobSDK(t, reader)
	assertJobSDK(t, first, want)
	assertJobSDK(t, second, want)
	// The SDK caller owns every returned slice. Mutate both a datapoint slice
	// and nested histogram slices, then read again without republishing a frame.
	first["jelee.jobs.shared.outcomes"].Data.(metricdata.Sum[int64]).DataPoints[0].Value = -1
	first["jelee.jobs.shared.queued"].Data.(metricdata.Gauge[int64]).DataPoints[0].Value = -1
	first["jelee.jobs.shared.oldest_queued_age"].Data.(metricdata.Gauge[float64]).DataPoints[0].Value = -1
	hist := first["jelee.jobs.shared.initial_wait"].Data.(metricdata.Histogram[float64])
	hist.DataPoints[0].Bounds[0] = -1
	hist.DataPoints[0].BucketCounts[0] = 999999
	hist.DataPoints[0].Sum = -1
	assertJobSDK(t, second, want)
	assertJobSDK(t, collectJobSDK(t, reader), want)
	if frame.snapshot != want {
		t.Fatal("reader mutated the published snapshot")
	}
	// A new collection supplies absolute totals. Neither a second collection
	// of one frame nor a new frame must add the old observations a second time.
	changed := jobMetricsTestSnapshot(2)
	changed.ObservedAt = changed.ObservedAt.AddDate(0, 0, 1)
	frame = jobSDKFrame(p, changed, context.Background())
	frame.gatherStarted.Store(true)
	assertJobSDK(t, collectJobSDK(t, reader), changed)
	// Clearing the frame must not invalidate data already owned by the reader.
	p.active.Store(nil)
	assertJobSDK(t, second, want)
	var absent metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &absent); !errors.Is(err, errJobMetricsUnavailable) || len(absent.ScopeMetrics) != 0 {
		t.Fatal("unprepared SDK producer fabricated or reused job metrics", err)
	}
}

type jobSDKProducerFunc func(context.Context) ([]metricdata.ScopeMetrics, error)

func (f jobSDKProducerFunc) Produce(ctx context.Context) ([]metricdata.ScopeMetrics, error) {
	return f(ctx)
}

// This fixture deliberately uses the real exporter and SDK. The fixed local
// gauge lets tests prove that an SDK error can leave a partial successful gather.
func jobSDKRegistry(t *testing.T, p sdkmetric.Producer, callbackErr error) *prometheus.Registry {
	t.Helper()
	registry := prometheus.NewRegistry()
	exporter, err := otelexport.New(otelexport.WithRegisterer(registry), otelexport.WithProducer(p),
		otelexport.WithoutTargetInfo(), otelexport.WithoutScopeInfo(),
		otelexport.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes))
	if err != nil {
		t.Fatal(err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	meter := provider.Meter("job-sdk-fixture")
	gauge, err := meter.Int64ObservableGauge("jelee.fixture.local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		observer.ObserveInt64(gauge, 7)
		return callbackErr
	}, gauge); err != nil {
		t.Fatal(err)
	}
	return registry
}

func serveJobSDKGuard(gatherer prometheus.Gatherer) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{DisableCompression: true, ErrorHandling: promhttp.HTTPErrorOnError}).ServeHTTP(
		w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return w
}

func assertJobSDKSafeFailure(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	const safeBody = "An error has occurred while serving metrics:\n\nmetrics unavailable\n"
	if w.Code != http.StatusInternalServerError || w.Body.String() != safeBody {
		t.Fatalf("failed gather returned partial metrics or exposed its error: status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestJobSDKGuardRejectsExporterSwallowedErrors(t *testing.T) {
	for _, failure := range []string{"sdk-callback", "external-producer"} {
		t.Run(failure, func(t *testing.T) {
			p := &jobMetricsProducer{}
			var external sdkmetric.Producer = p
			var callbackErr error
			if failure == "sdk-callback" {
				callbackErr = errJobMetricsUnavailable
			} else {
				external = jobSDKProducerFunc(func(context.Context) ([]metricdata.ScopeMetrics, error) {
					return nil, errJobMetricsUnavailable
				})
			}
			registry := jobSDKRegistry(t, external, callbackErr)
			frame := jobSDKFrame(p, jobMetricsTestSnapshot(1), context.Background())
			frame.gatherStarted.Store(true)
			partial, err := registry.Gather()
			if err != nil || len(partial) != 1 || partial[0].GetName() != "jelee_fixture_local" || frame.produced.Load() != 0 {
				t.Fatal("fixture did not reproduce the real exporter's swallowed error", err)
			}
			guard := jobMetricsGatherer{registry: registry, producer: p}
			jobSDKFrame(p, jobMetricsTestSnapshot(1), context.Background())
			if families, err := guard.Gather(); err == nil || families != nil {
				t.Fatal("guard returned the exporter's partial local family", err)
			}
			jobSDKFrame(p, jobMetricsTestSnapshot(1), context.Background())
			assertJobSDKSafeFailure(t, serveJobSDKGuard(guard))
		})
	}
}

type jobSDKGathererFunc func() ([]*dto.MetricFamily, error)

func (f jobSDKGathererFunc) Gather() ([]*dto.MetricFamily, error) { return f() }

func jobSDKFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatal("real exporter did not produce the fixture family", name)
	return nil
}

func TestJobSDKGuardRejectsIncompleteOrInvalidatedGathers(t *testing.T) {
	for _, failure := range []string{"missing-receipt", "missing-family", "wrong-type", "missing-label", "unknown-label", "duplicate-point", "replaced-frame", "cleared-frame", "cancelled-context", "registry-error"} {
		t.Run(failure, func(t *testing.T) {
			p := &jobMetricsProducer{}
			registry := jobSDKRegistry(t, p, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			frame := jobSDKFrame(p, jobMetricsTestSnapshot(1), ctx)
			guard := jobMetricsGatherer{producer: p, registry: jobSDKGathererFunc(func() ([]*dto.MetricFamily, error) {
				families, err := registry.Gather()
				if err != nil || frame.produced.Load() != 1 {
					t.Fatal("real exporter did not complete the valid fixture before fault injection", err)
				}
				switch failure {
				case "missing-receipt":
					frame.produced.Store(0)
				case "missing-family":
					for i, family := range families {
						if family.GetName() == "jelee_jobs_shared_duration_seconds" {
							families = append(families[:i], families[i+1:]...)
							break
						}
					}
				case "wrong-type":
					jobSDKFamily(t, families, "jelee_jobs_shared_queued").Type = dto.MetricType_COUNTER.Enum()
				case "missing-label":
					point := jobSDKFamily(t, families, "jelee_jobs_shared_queued").Metric[0]
					point.Label = point.Label[:1]
				case "unknown-label":
					value := "secret-library-path"
					jobSDKFamily(t, families, "jelee_jobs_shared_queued").Metric[0].Label[0].Value = &value
				case "duplicate-point":
					family := jobSDKFamily(t, families, "jelee_jobs_shared_outcomes_total")
					family.Metric[1] = family.Metric[0]
				case "replaced-frame":
					jobSDKFrame(p, jobMetricsTestSnapshot(1), ctx)
				case "cleared-frame":
					p.active.Store(nil)
				case "cancelled-context":
					cancel()
				case "registry-error":
					return families, errors.New("secret DSN=postgres://private/path SQL fixture")
				}
				return families, nil
			})}
			assertJobSDKSafeFailure(t, serveJobSDKGuard(guard))
		})
	}
	// No active frame, an already-used frame and pre-cancellation must fail
	// before touching even the in-memory registry.
	for _, failure := range []string{"missing-frame", "already-gathered", "pre-cancelled"} {
		t.Run(failure, func(t *testing.T) {
			p := &jobMetricsProducer{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure != "missing-frame" {
				frame := jobSDKFrame(p, jobMetricsTestSnapshot(1), ctx)
				if failure == "already-gathered" {
					frame.gatherStarted.Store(true)
				} else {
					cancel()
				}
			}
			calls := 0
			guard := jobMetricsGatherer{producer: p, registry: jobSDKGathererFunc(func() ([]*dto.MetricFamily, error) {
				calls++
				return nil, nil
			})}
			assertJobSDKSafeFailure(t, serveJobSDKGuard(guard))
			if calls != 0 {
				t.Fatal("unprepared gather reached the registry")
			}
		})
	}
}

func TestJobSDKAdditionalReaderCannotFillHTTPReceipt(t *testing.T) {
	privateProducer := &jobMetricsProducer{}
	privateReader := sdkmetric.NewManualReader(sdkmetric.WithProducer(privateProducer))
	privateSnapshot := jobMetricsTestSnapshot(2)
	privateFrame := jobSDKFrame(privateProducer, privateSnapshot, context.Background())
	privateFrame.gatherStarted.Store(true)
	httpSnapshot := jobMetricsTestSnapshot(1)
	source := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) { return httpSnapshot, nil }}
	var m *Metrics
	var httpFrame *jobMetricCollection
	pool := &testPool{value: samplePool(), read: func() {
		if m != nil && m.jobsProducer.active.Load() != nil {
			httpFrame = m.jobsProducer.active.Load()
		}
	}}
	var err error
	m, err = newMetricsWithJobs(pool, source, sampleRuntime, privateReader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	if m.jobsProducer == privateProducer {
		t.Fatal("manual reader shares the HTTP producer")
	}
	assertJobSDK(t, collectJobSDK(t, privateReader), privateSnapshot)
	if privateFrame.produced.Load() != 1 || m.jobsProducer.active.Load() != nil || source.calls.Load() != 0 {
		t.Fatal("manual collection entered HTTP admission or populated its receipt")
	}
	families, _ := scrapeJobMetrics(t, m)
	assertJobMetricsExposition(t, families, httpSnapshot)
	if httpFrame == nil || httpFrame.produced.Load() != 1 || privateFrame.produced.Load() != 1 || source.calls.Load() != 1 {
		t.Fatal("HTTP collection used the other reader's source or receipt")
	}
	assertJobSDK(t, collectJobSDK(t, privateReader), privateSnapshot)
	if privateFrame.produced.Load() != 2 || httpFrame.produced.Load() != 1 || m.jobsProducer.active.Load() != nil {
		t.Fatal("later manual collection changed the completed HTTP receipt")
	}
}
