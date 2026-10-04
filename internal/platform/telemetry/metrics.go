// Package telemetry exposes local runtime and pool snapshots through a private
// OpenTelemetry provider and Prometheus registry.
package telemetry

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/otel/attribute"
	otelexport "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// Metrics owns its instruments, provider, and HTTP exporter. It starts no
// polling workers and does not register anything with global providers.
type Metrics struct {
	provider     *sdkmetric.MeterProvider
	jobsProducer *jobMetricsProducer
	handler      http.Handler
	gate         chan struct{}
	scrapeGate   chan struct{}
	stopScrapes  chan struct{}
	stopOnce     sync.Once
	stopped      atomic.Bool
	shutdownGate chan struct{}
	shutdownDone bool
	shutdownErr  error
	// ops is the shared operational source registered before serving;
	// opsActive holds the snapshot prefetched for the gather in progress.
	ops       atomic.Pointer[opsSource]
	opsActive atomic.Pointer[app.OpsMetricsSnapshot]
}

type runtimeSnapshot struct {
	heapBytes      uint64
	allocatedBytes uint64
	gcPauseNS      uint64
	gcCycles       uint32
	goroutines     int
}

func readRuntime() runtimeSnapshot {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return runtimeSnapshot{
		heapBytes: mem.HeapAlloc, allocatedBytes: mem.TotalAlloc,
		gcPauseNS: mem.PauseTotalNs, gcCycles: mem.NumGC,
		goroutines: runtime.NumGoroutine(),
	}
}

// New creates a private exporter. PoolStats must only read local state; the
// callback runs during collection and must not perform I/O.
func New(pool app.PoolStatsSource) (*Metrics, error) {
	return newMetrics(pool, readRuntime)
}

// NewWithJobs adds shared database job metrics. JobMetrics must honor its
// context; it is called once per admitted request, before SDK collection.
func NewWithJobs(pool app.PoolStatsSource, jobs app.JobMetricsSource) (*Metrics, error) {
	if jobs == nil {
		return nil, errors.New("telemetry requires a job metrics source")
	}
	return newMetricsWithJobs(pool, jobs, readRuntime)
}

func newMetrics(pool app.PoolStatsSource, read func() runtimeSnapshot, readers ...sdkmetric.Reader) (*Metrics, error) {
	return newMetricsWithJobs(pool, nil, read, readers...)
}

func newMetricsWithJobs(pool app.PoolStatsSource, jobs app.JobMetricsSource, read func() runtimeSnapshot, readers ...sdkmetric.Reader) (*Metrics, error) {
	if pool == nil || read == nil {
		return nil, errors.New("telemetry requires snapshot sources")
	}
	registry := prometheus.NewRegistry()
	exporterOptions := []otelexport.Option{
		otelexport.WithRegisterer(registry),
		otelexport.WithoutTargetInfo(),
		otelexport.WithoutScopeInfo(),
		otelexport.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes),
	}
	var producer *jobMetricsProducer
	if jobs != nil {
		producer = &jobMetricsProducer{}
		exporterOptions = append(exporterOptions, otelexport.WithProducer(producer))
	}
	exporter, err := otelexport.New(exporterOptions...)
	if err != nil {
		return nil, err
	}
	// The SDK can merge environment resource attributes. The exporter above
	// suppresses resource/scope output; never enable resource constant labels.
	options := []sdkmetric.Option{
		sdkmetric.WithReader(exporter),
		sdkmetric.WithResource(resource.NewSchemaless(attribute.String("service.name", "jelee"))),
	}
	for _, reader := range readers {
		options = append(options, sdkmetric.WithReader(reader))
	}
	m := &Metrics{
		provider: sdkmetric.NewMeterProvider(options...), gate: make(chan struct{}, 1),
		scrapeGate: make(chan struct{}, 1), stopScrapes: make(chan struct{}),
		shutdownGate: make(chan struct{}, 1), jobsProducer: producer,
	}
	m.gate <- struct{}{}
	m.scrapeGate <- struct{}{}
	m.shutdownGate <- struct{}{}
	if err = m.register(pool, read); err != nil {
		return nil, errors.Join(err, m.provider.Shutdown(context.Background()))
	}
	var gatherer prometheus.Gatherer = registry
	if producer != nil {
		gatherer = jobMetricsGatherer{registry: registry, producer: producer}
	}
	serve := promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		DisableCompression: true, ErrorHandling: promhttp.HTTPErrorOnError,
	})
	m.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The exporter collects with context.TODO(), so wait outside it using
		// the request context. Only one request enters a real gather at a time.
		select {
		case <-r.Context().Done():
			http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
			return
		case <-m.stopScrapes:
			http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
			return
		case <-m.scrapeGate:
		}
		defer func() { m.scrapeGate <- struct{}{} }()
		// A ready gate can win a select against cancellation or shutdown.
		// Recheck before admitting this request to the exporter.
		select {
		case <-r.Context().Done():
			http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
			return
		case <-m.stopScrapes:
			http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
			return
		default:
		}
		// Database prefetches share one two-second budget.
		prefetchDeadline := time.Now().Add(2 * time.Second)
		if jobs != nil {
			prefetchCtx, cancel := context.WithDeadline(r.Context(), prefetchDeadline)
			snapshot, err := jobs.JobMetrics(prefetchCtx)
			contextErr := prefetchCtx.Err()
			cancel()
			if err != nil || contextErr != nil || !validJobSnapshot(snapshot) {
				http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
				return
			}
			select {
			case <-r.Context().Done():
				http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
				return
			case <-m.stopScrapes:
				http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
				return
			default:
			}
			producer.active.Store(&jobMetricCollection{snapshot: snapshot, requestContext: r.Context()})
			defer producer.active.Store(nil)
		}
		if source := m.ops.Load(); source != nil {
			snapshot, ok := source.prefetch(r.Context(), prefetchDeadline)
			if !ok || r.Context().Err() != nil {
				http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
				return
			}
			m.opsActive.Store(&snapshot)
			defer m.opsActive.Store(nil)
		}
		serve.ServeHTTP(w, r)
	})
	return m, nil
}

// Handler returns the uninstrumented private exporter. The HTTP adapter owns
// authentication, admission, request limits, and response deadlines. Waiting
// scrapes are cancelable. Database prefetch has a two-second context, which the
// source must honor; an executing local snapshot is synchronous and cannot be
// interrupted by the request deadline.
func (m *Metrics) Handler() http.Handler { return m.handler }

// Shutdown waits for admitted scrapes and local snapshots before stopping the
// provider. After a successful shutdown no source can read the pool. A canceled
// gate wait can be retried; admission stays closed once shutdown begins. The
// provider shutdown result is cached, including any error from the SDK.
func (m *Metrics) Shutdown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.stopOnce.Do(func() { close(m.stopScrapes) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.shutdownGate:
	}
	defer func() { m.shutdownGate <- struct{}{} }()
	if m.shutdownDone {
		return m.shutdownErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.scrapeGate:
	}
	select {
	case <-ctx.Done():
		m.scrapeGate <- struct{}{}
		return ctx.Err()
	case <-m.gate:
	}
	if err := ctx.Err(); err != nil {
		m.gate <- struct{}{}
		m.scrapeGate <- struct{}{}
		return err
	}
	m.stopped.Store(true)
	if m.jobsProducer != nil {
		m.jobsProducer.active.Store(nil)
	}
	// Release both collection gates before SDK shutdown. A reader may
	// already hold the SDK collection lock and be waiting for our callback.
	m.gate <- struct{}{}
	m.scrapeGate <- struct{}{}
	m.shutdownErr = m.provider.Shutdown(ctx)
	m.shutdownDone = true
	return m.shutdownErr
}

type snapshot struct {
	runtime runtimeSnapshot
	pool    app.PoolStatsSnapshot
}

type intMetric struct {
	name, description string
	counter           bool
	value             func(snapshot) int64
	instrument        metric.Int64Observable
}

type floatMetric struct {
	name, unit, description string
	counter                 bool
	value                   func(snapshot) float64
	instrument              metric.Float64Observable
}

func (m *Metrics) register(pool app.PoolStatsSource, read func() runtimeSnapshot) error {
	ints := []intMetric{
		{name: "jelee.runtime.goroutines", description: "Current goroutines.", value: func(s snapshot) int64 { return int64(s.runtime.goroutines) }},
		{name: "jelee.runtime.gc.cycles", description: "Completed garbage collection cycles since process start.", counter: true, value: func(s snapshot) int64 { return int64(s.runtime.gcCycles) }},
		{name: "jelee.db.pool.connections.acquired", description: "Currently acquired pool connections.", value: func(s snapshot) int64 { return int64(s.pool.AcquiredConns) }},
		{name: "jelee.db.pool.connections.idle", description: "Currently idle pool connections.", value: func(s snapshot) int64 { return int64(s.pool.IdleConns) }},
		{name: "jelee.db.pool.connections.constructing", description: "Pool connections being constructed.", value: func(s snapshot) int64 { return int64(s.pool.ConstructingConns) }},
		{name: "jelee.db.pool.connections.total", description: "Current pool connections including construction.", value: func(s snapshot) int64 { return int64(s.pool.TotalConns) }},
		{name: "jelee.db.pool.connections.max", description: "Configured maximum pool connections.", value: func(s snapshot) int64 { return int64(s.pool.MaxConns) }},
		{name: "jelee.db.pool.acquire.success", description: "Successful pool acquisitions since pool creation.", counter: true, value: func(s snapshot) int64 { return s.pool.AcquireCount }},
		{name: "jelee.db.pool.acquire.canceled", description: "Pool acquisitions canceled by context since pool creation.", counter: true, value: func(s snapshot) int64 { return s.pool.CanceledAcquireCount }},
		{name: "jelee.db.pool.acquire.empty", description: "Successful pool acquisitions that waited for an empty pool since pool creation.", counter: true, value: func(s snapshot) int64 { return s.pool.EmptyAcquireCount }},
	}
	floats := []floatMetric{
		{name: "jelee.runtime.heap", unit: "By", description: "Current allocated heap bytes.", value: func(s snapshot) float64 { return float64(s.runtime.heapBytes) }},
		{name: "jelee.runtime.allocated", unit: "By", description: "Cumulative allocated heap bytes since process start.", counter: true, value: func(s snapshot) float64 { return float64(s.runtime.allocatedBytes) }},
		{name: "jelee.runtime.gc.pause", unit: "s", description: "Cumulative garbage collection pause seconds since process start.", counter: true, value: func(s snapshot) float64 { return float64(s.runtime.gcPauseNS) / 1e9 }},
		{name: "jelee.db.pool.acquire.duration", unit: "s", description: "Cumulative duration of successful pool acquisitions in seconds.", counter: true, value: func(s snapshot) float64 { return s.pool.AcquireDuration.Seconds() }},
		{name: "jelee.db.pool.acquire.empty.wait", unit: "s", description: "Cumulative wait seconds for successful empty pool acquisitions; excludes canceled waits.", counter: true, value: func(s snapshot) float64 { return s.pool.EmptyAcquireWaitTime.Seconds() }},
	}
	meter := m.provider.Meter("github.com/MoYuanCN/Jelee/internal/platform/telemetry")
	instruments := make([]metric.Observable, 0, len(ints)+len(floats))
	for i := range ints {
		d := &ints[i]
		var err error
		if d.counter {
			d.instrument, err = meter.Int64ObservableCounter(d.name, metric.WithDescription(d.description))
		} else {
			d.instrument, err = meter.Int64ObservableGauge(d.name, metric.WithDescription(d.description))
		}
		if err != nil {
			return err
		}
		instruments = append(instruments, d.instrument)
	}
	for i := range floats {
		d := &floats[i]
		var err error
		if d.counter {
			d.instrument, err = meter.Float64ObservableCounter(d.name, metric.WithUnit(d.unit), metric.WithDescription(d.description))
		} else {
			d.instrument, err = meter.Float64ObservableGauge(d.name, metric.WithUnit(d.unit), metric.WithDescription(d.description))
		}
		if err != nil {
			return err
		}
		instruments = append(instruments, d.instrument)
	}
	_, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.gate:
		}
		defer func() { m.gate <- struct{}{} }()
		if m.stopped.Load() {
			return nil
		}
		s := snapshot{runtime: read(), pool: pool.PoolStats()}
		for _, d := range ints {
			observer.ObserveInt64(d.instrument, d.value(s))
		}
		for _, d := range floats {
			observer.ObserveFloat64(d.instrument, d.value(s))
		}
		return nil
	}, instruments...)
	return err
}
