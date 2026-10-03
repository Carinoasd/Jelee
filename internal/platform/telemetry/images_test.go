package telemetry

import (
	"context"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var _ ImageStatsSource = (*imageadapter.Processor)(nil)

type imageTestSource struct {
	mu        sync.Mutex
	processor imageadapter.Stats
	store     imageadapter.StoreStats
	withStore bool
}

func (s *imageTestSource) Stats() imageadapter.Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processor
}

func (s *imageTestSource) StoreStats() (imageadapter.StoreStats, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.withStore {
		return imageadapter.StoreStats{}, false
	}
	return s.store, true
}

func (s *imageTestSource) set(scale uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processor, s.store = imageTestStats(scale)
}

// Every field gets a distinct value so a swapped mapping cannot pass.
func imageTestStats(scale uint64) (imageadapter.Stats, imageadapter.StoreStats) {
	n := func(i uint64) uint64 { return i * scale }
	i := func(v uint64) int64 { return int64(n(v)) }
	return imageadapter.Stats{
		Active: i(1), ReservedBytes: i(2), MaxEstimatedImageBytes: i(3),
		Admitted: n(4), Completed: n(5), Failed: n(6), Busy: n(7),
		CacheHits: n(8), CacheMisses: n(9), Decodes: n(10),
		CacheEntries: int(n(11)), CacheBytes: i(12), CacheEvictions: n(13),
		VariantHits: n(14), StoreFailures: n(15), IndexFailures: n(16), IndexEvictions: n(17),
	}, imageadapter.StoreStats{
		OriginalEntries: int(n(18)), VariantEntries: int(n(19)), OriginalBytes: i(20), VariantBytes: i(21),
		OriginalHits: n(22), OriginalMisses: n(23), VariantHits: n(24), VariantMisses: n(25),
		Evictions: n(26), Corrupt: n(27), Deduplicated: n(28), Cleared: n(29),
		RemoveErrors: n(30), Foreign: n(31),
	}
}

type imageExpectation struct {
	prom, otel string
	counter    bool
	// Unlabelled metrics return one value; class metrics return original, variant.
	values func(imageadapter.Stats, imageadapter.StoreStats) []float64
}

func one(v float64) []float64 { return []float64{v} }

// The expectation table is written against the adapter fields, independently
// of the implementation table, and pins the exposed Prometheus names.
func imageProcessorExpectations() []imageExpectation {
	type p = imageadapter.Stats
	type s = imageadapter.StoreStats
	return []imageExpectation{
		{"jelee_images_active", "jelee.images.active", false, func(a p, _ s) []float64 { return one(float64(a.Active)) }},
		{"jelee_images_reserved_bytes", "jelee.images.reserved", false, func(a p, _ s) []float64 { return one(float64(a.ReservedBytes)) }},
		{"jelee_images_requests_admitted_total", "jelee.images.requests.admitted", true, func(a p, _ s) []float64 { return one(float64(a.Admitted)) }},
		{"jelee_images_requests_completed_total", "jelee.images.requests.completed", true, func(a p, _ s) []float64 { return one(float64(a.Completed)) }},
		{"jelee_images_requests_failed_total", "jelee.images.requests.failed", true, func(a p, _ s) []float64 { return one(float64(a.Failed)) }},
		{"jelee_images_requests_rejected_total", "jelee.images.requests.rejected", true, func(a p, _ s) []float64 { return one(float64(a.Busy)) }},
		{"jelee_images_decodes_total", "jelee.images.decodes", true, func(a p, _ s) []float64 { return one(float64(a.Decodes)) }},
		{"jelee_images_cache_hits_total", "jelee.images.cache.hits", true, func(a p, _ s) []float64 { return one(float64(a.CacheHits)) }},
		{"jelee_images_cache_misses_total", "jelee.images.cache.misses", true, func(a p, _ s) []float64 { return one(float64(a.CacheMisses)) }},
		{"jelee_images_cache_evictions_total", "jelee.images.cache.evictions", true, func(a p, _ s) []float64 { return one(float64(a.CacheEvictions)) }},
		{"jelee_images_cache_entries", "jelee.images.cache.entries", false, func(a p, _ s) []float64 { return one(float64(a.CacheEntries)) }},
		{"jelee_images_cache_usage_bytes", "jelee.images.cache.usage", false, func(a p, _ s) []float64 { return one(float64(a.CacheBytes)) }},
	}
}

func imageStoreExpectations() []imageExpectation {
	type p = imageadapter.Stats
	type s = imageadapter.StoreStats
	return []imageExpectation{
		{"jelee_images_store_hits_total", "jelee.images.store.hits", true, func(_ p, b s) []float64 { return []float64{float64(b.OriginalHits), float64(b.VariantHits)} }},
		{"jelee_images_store_misses_total", "jelee.images.store.misses", true, func(_ p, b s) []float64 { return []float64{float64(b.OriginalMisses), float64(b.VariantMisses)} }},
		{"jelee_images_store_entries", "jelee.images.store.entries", false, func(_ p, b s) []float64 {
			return []float64{float64(b.OriginalEntries), float64(b.VariantEntries)}
		}},
		{"jelee_images_store_usage_bytes", "jelee.images.store.usage", false, func(_ p, b s) []float64 { return []float64{float64(b.OriginalBytes), float64(b.VariantBytes)} }},
		{"jelee_images_store_evictions_total", "jelee.images.store.evictions", true, func(_ p, b s) []float64 { return one(float64(b.Evictions)) }},
		{"jelee_images_store_corrupt_total", "jelee.images.store.corrupt", true, func(_ p, b s) []float64 { return one(float64(b.Corrupt)) }},
		{"jelee_images_store_deduplicated_total", "jelee.images.store.deduplicated", true, func(_ p, b s) []float64 { return one(float64(b.Deduplicated)) }},
		{"jelee_images_store_cleared_total", "jelee.images.store.cleared", true, func(_ p, b s) []float64 { return one(float64(b.Cleared)) }},
		{"jelee_images_store_remove_errors_total", "jelee.images.store.remove_errors", true, func(_ p, b s) []float64 { return one(float64(b.RemoveErrors)) }},
		{"jelee_images_store_foreign_total", "jelee.images.store.foreign", true, func(_ p, b s) []float64 { return one(float64(b.Foreign)) }},
		{"jelee_images_store_served_total", "jelee.images.store.served", true, func(a p, _ s) []float64 { return one(float64(a.VariantHits)) }},
		{"jelee_images_store_failures_total", "jelee.images.store.failures", true, func(a p, _ s) []float64 { return one(float64(a.StoreFailures)) }},
		{"jelee_images_index_failures_total", "jelee.images.index.failures", true, func(a p, _ s) []float64 { return one(float64(a.IndexFailures)) }},
		{"jelee_images_index_evictions_total", "jelee.images.index.evictions", true, func(a p, _ s) []float64 { return one(float64(a.IndexEvictions)) }},
	}
}

func newImageTestExporter(t *testing.T, source ImageStatsSource) *Metrics {
	t.Helper()
	b, err := resources.New(resources.Limits{CPU: 1, IO: 2, Total: 2, Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	jobs := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) { return jobMetricsTestSnapshot(1), nil }}
	m, err := NewWithImages(&testPool{value: samplePool()}, jobs, b, source)
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

func scrapeImageMetrics(t *testing.T, m *Metrics) map[string]*dto.MetricFamily {
	t.Helper()
	w := requestJobMetrics(m, context.Background())
	if w.Code != http.StatusOK || w.Body.Len() > 64*1024 {
		t.Fatalf("image scrape status/size: %d/%d", w.Code, w.Body.Len())
	}
	body := w.Body.String()
	for _, forbidden := range []string{"path", "digest", "source", "item", "id=", "/"} {
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "jelee_images_") && strings.Contains(line, "{") && strings.Contains(line[strings.Index(line, "{"):], forbidden) {
				t.Fatalf("image metric carries a sensitive label (%q): %s", forbidden, line)
			}
		}
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return families
}

func imageSampleValue(t *testing.T, name string, counter bool, sample *dto.Metric) float64 {
	t.Helper()
	if counter {
		if sample.Counter == nil {
			t.Fatalf("%s must be a counter", name)
		}
		return sample.GetCounter().GetValue()
	}
	if sample.Gauge == nil {
		t.Fatalf("%s must be a gauge", name)
	}
	return sample.GetGauge().GetValue()
}

func assertImageFamilies(t *testing.T, families map[string]*dto.MetricFamily, expectations []imageExpectation, processor imageadapter.Stats, store imageadapter.StoreStats) {
	t.Helper()
	for _, e := range expectations {
		family := families[e.prom]
		want := e.values(processor, store)
		if family == nil || len(family.Metric) != len(want) {
			t.Fatalf("image metric %s missing or with wrong series count", e.prom)
		}
		wantType := dto.MetricType_GAUGE
		if e.counter {
			wantType = dto.MetricType_COUNTER
		}
		if family.GetType() != wantType {
			t.Fatalf("image metric %s type %v, want %v", e.prom, family.GetType(), wantType)
		}
		for _, sample := range family.Metric {
			index := 0
			if len(want) == 1 {
				if len(sample.Label) != 0 {
					t.Fatalf("image metric %s must be unlabelled: %v", e.prom, sample.Label)
				}
			} else {
				if len(sample.Label) != 1 || sample.Label[0].GetName() != "class" {
					t.Fatalf("image metric %s may only carry the class label: %v", e.prom, sample.Label)
				}
				switch sample.Label[0].GetValue() {
				case "original":
				case "variant":
					index = 1
				default:
					t.Fatalf("image metric %s unexpected class %q", e.prom, sample.Label[0].GetValue())
				}
			}
			if got := imageSampleValue(t, e.prom, e.counter, sample); got != want[index] {
				t.Fatalf("image metric %s[%d] = %v, want %v", e.prom, index, got, want[index])
			}
		}
	}
}

func TestImageMetricsFollowProcessorAndStoreStats(t *testing.T) {
	source := &imageTestSource{withStore: true}
	source.set(1)
	m := newImageTestExporter(t, source)
	expectations := append(imageProcessorExpectations(), imageStoreExpectations()...)
	for _, scale := range []uint64{1, 3, 7} {
		source.set(scale)
		processor, store := imageTestStats(scale)
		families := scrapeImageMetrics(t, m)
		if len(families) != 30+len(expectations) {
			t.Fatalf("metric families: %d, want %d", len(families), 30+len(expectations))
		}
		assertImageFamilies(t, families, expectations, processor, store)
	}
}

func TestImageMetricsOmitDisabledStore(t *testing.T) {
	source := &imageTestSource{}
	source.set(2)
	m := newImageTestExporter(t, source)
	processor, _ := imageTestStats(2)
	families := scrapeImageMetrics(t, m)
	if len(families) != 30+len(imageProcessorExpectations()) {
		t.Fatalf("metric families: %d, want %d", len(families), 30+len(imageProcessorExpectations()))
	}
	for name := range families {
		if strings.HasPrefix(name, "jelee_images_store_") || strings.HasPrefix(name, "jelee_images_index_") {
			t.Fatalf("disabled store exposed %s", name)
		}
	}
	assertImageFamilies(t, families, imageProcessorExpectations(), processor, imageadapter.StoreStats{})
}

func TestImageMetricsNilSourceRegistersNothing(t *testing.T) {
	families := scrapeImageMetrics(t, newImageTestExporter(t, nil))
	if len(families) != 30 {
		t.Fatalf("metric families: %d, want 30", len(families))
	}
	for name := range families {
		if strings.HasPrefix(name, "jelee_images_") {
			t.Fatalf("nil image source exposed %s", name)
		}
	}
}

func TestImageMetricsSDKValuesAndAttributes(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	m := testMetrics(t, &testPool{value: samplePool()}, sampleRuntime, reader)
	source := &imageTestSource{withStore: true}
	if err := m.registerImages(source); err != nil {
		t.Fatal(err)
	}
	expectations := append(imageProcessorExpectations(), imageStoreExpectations()...)
	classes := []attribute.Set{
		attribute.NewSet(attribute.String("class", "original")),
		attribute.NewSet(attribute.String("class", "variant")),
	}
	for _, scale := range []uint64{0, 5, 11} {
		source.set(scale)
		processor, store := imageTestStats(scale)
		var collected metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &collected); err != nil {
			t.Fatal(err)
		}
		got := map[string]metricdata.Metrics{}
		for _, scope := range collected.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if strings.HasPrefix(metric.Name, "jelee.images.") {
					if _, duplicate := got[metric.Name]; duplicate {
						t.Fatal("duplicate image metric", metric.Name)
					}
					got[metric.Name] = metric
				}
			}
		}
		if len(got) != len(expectations) {
			t.Fatalf("SDK returned %d image metrics, want %d", len(got), len(expectations))
		}
		for _, e := range expectations {
			metric := got[e.otel]
			var points []metricdata.DataPoint[int64]
			switch data := metric.Data.(type) {
			case metricdata.Sum[int64]:
				if !e.counter || !data.IsMonotonic || data.Temporality != metricdata.CumulativeTemporality {
					t.Fatalf("%s must be a cumulative monotonic counter", e.otel)
				}
				points = data.DataPoints
			case metricdata.Gauge[int64]:
				if e.counter {
					t.Fatalf("%s must be a counter", e.otel)
				}
				points = data.DataPoints
			default:
				t.Fatalf("%s has unexpected data %T", e.otel, metric.Data)
			}
			want := e.values(processor, store)
			if len(points) != len(want) {
				t.Fatalf("%s has %d points, want %d", e.otel, len(points), len(want))
			}
			for _, point := range points {
				index := 0
				if len(want) == 1 {
					if point.Attributes.Len() != 0 {
						t.Fatalf("%s must carry no attributes: %v", e.otel, point.Attributes)
					}
				} else if point.Attributes.Equals(&classes[1]) {
					index = 1
				} else if !point.Attributes.Equals(&classes[0]) {
					t.Fatalf("%s carries unexpected attributes: %v", e.otel, point.Attributes)
				}
				if float64(point.Value) != want[index] {
					t.Fatalf("%s[%d] = %d, want %v", e.otel, index, point.Value, want[index])
				}
			}
		}
	}
}

func TestImageMetricsSaturateLargeCounts(t *testing.T) {
	if imageCount(math.MaxUint64) != math.MaxInt64 || imageCount(math.MaxInt64) != math.MaxInt64 || imageCount(42) != 42 {
		t.Fatal("image counts must saturate at MaxInt64")
	}
}

func TestImageMetricsStopReadingAfterShutdown(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	m, err := newMetrics(&testPool{value: samplePool()}, sampleRuntime, reader)
	if err != nil {
		t.Fatal(err)
	}
	source := &countingImageSource{}
	if err := m.registerImages(source); err != nil {
		t.Fatal(err)
	}
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil || source.calls() == 0 {
		t.Fatal("image source was not read during collection", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := source.calls()
	_ = reader.Collect(context.Background(), &collected)
	if source.calls() != before {
		t.Fatal("image source read after shutdown")
	}
}

type countingImageSource struct {
	mu sync.Mutex
	n  int
}

func (s *countingImageSource) Stats() imageadapter.Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return imageadapter.Stats{}
}

func (s *countingImageSource) StoreStats() (imageadapter.StoreStats, bool) {
	return imageadapter.StoreStats{}, false
}

func (s *countingImageSource) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// A real processor and store: lookups change the exposed hit/miss counters.
func TestImageMetricsRealProcessorAndStore(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("private store directories are exercised on Linux")
	}
	base := t.TempDir()
	temp, root := filepath.Join(base, "temp"), filepath.Join(base, "store")
	for _, directory := range []string{temp, root} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := imageadapter.OpenStore(context.Background(), imageadapter.StoreOptions{Root: root, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024})
	if err != nil {
		t.Fatal("open real image store", err)
	}
	processor, err := imageadapter.New(context.Background(), imageadapter.Options{TempRoot: temp, MaxConcurrent: 2, MaxImageBytes: 96 << 20, MaxSourceBytes: 16 << 20,
		MaxOutputBytes: 2 << 20, CacheBytes: 32 << 20, MaxOutputDimension: 1024, CacheEntries: 128,
		DefaultQuality: 85, Timeout: 15 * time.Second, CacheTTL: 300 * time.Second, Store: store})
	if err != nil {
		t.Fatal("create real image processor", err)
	}
	t.Cleanup(func() {
		if err := processor.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
		if err := store.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	m := newImageTestExporter(t, processor)
	value := func(families map[string]*dto.MetricFamily, name, class string) float64 {
		t.Helper()
		family := families[name]
		if family == nil {
			t.Fatalf("real image metric %s missing", name)
		}
		for _, sample := range family.Metric {
			if class == "" || (len(sample.Label) == 1 && sample.Label[0].GetValue() == class) {
				return imageSampleValue(t, name, family.GetType() == dto.MetricType_COUNTER, sample)
			}
		}
		t.Fatalf("real image metric %s has no %q series", name, class)
		return 0
	}
	families := scrapeImageMetrics(t, m)
	if value(families, "jelee_images_store_misses_total", "variant") != 0 || value(families, "jelee_images_store_usage_bytes", "original") != 0 {
		t.Fatal("fresh store must start at zero")
	}
	var missing [32]byte
	missing[0] = 1
	if _, err := store.OpenVariant(context.Background(), missing, missing); err == nil {
		t.Fatal("missing variant opened")
	}
	digest, size, err := store.PutOriginal(context.Background(), strings.NewReader("original image bytes"), 1<<20)
	if err != nil {
		t.Fatal("store original", err)
	}
	object, err := store.OpenOriginal(context.Background(), digest, true)
	if err != nil {
		t.Fatal("open stored original", err)
	}
	if err := object.Close(); err != nil {
		t.Fatal(err)
	}
	families = scrapeImageMetrics(t, m)
	if value(families, "jelee_images_store_misses_total", "variant") != 1 ||
		value(families, "jelee_images_store_hits_total", "original") != 1 ||
		value(families, "jelee_images_store_entries", "original") != 1 ||
		value(families, "jelee_images_store_usage_bytes", "original") < float64(size) {
		t.Fatal("real store lookups were not reflected in metrics")
	}
	if value(families, "jelee_images_cache_hits_total", "") != 0 || value(families, "jelee_images_requests_admitted_total", "") != 0 {
		t.Fatal("idle processor reported activity")
	}
}
