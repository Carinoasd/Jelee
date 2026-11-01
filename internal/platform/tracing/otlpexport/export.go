// Package otlpexport sends the spans of the process tracer to an
// OpenTelemetry collector over OTLP/HTTP (G46.6, principle 5). It is off
// unless configured. The tracer hands it exactly the spans whose "span
// completed" record it logs, with the identifiers the logs carry, so the
// collector and the log store show the same traces and there is no second
// sampling decision. Spans wait in a bounded queue and leave in batches
// from one goroutine; a full queue drops instead of blocking the request or
// job that ended the span, and every loss is counted.
package otlpexport

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Drop reasons of the dropped counter.
const (
	DropQueueFull    = "queue_full"
	DropExportFailed = "export_failed"
	DropShutdown     = "shutdown"
)

// DropReasons lists every reason the dropped counter reports.
var DropReasons = []string{DropQueueFull, DropExportFailed, DropShutdown}

// Options configures an Exporter.
type Options struct {
	tracing.OTLPSettings
	// Logger receives the rate limited export failure warnings; nil
	// discards them.
	Logger *slog.Logger
}

func (o *Options) normalize() error {
	if err := o.Validate(); err != nil {
		return err
	}
	if o.Timeout == 0 {
		o.Timeout = tracing.OTLPDefaultTimeout
	}
	if o.BatchSize == 0 {
		o.BatchSize = tracing.OTLPDefaultBatchSize
	}
	if o.Interval == 0 {
		o.Interval = tracing.OTLPDefaultInterval
	}
	if o.QueueSize == 0 {
		o.QueueSize = tracing.OTLPDefaultQueueSize
	}
	return nil
}

// Exporter implements tracing.Exporter. Create it with New and stop it with
// Shutdown.
type Exporter struct {
	queue     chan tracing.SpanRecord
	exporter  sdktrace.SpanExporter
	convert   converter
	batchSize int
	interval  time.Duration
	timeout   time.Duration
	logger    *slog.Logger

	// mu orders ExportSpan against Shutdown: once closed is set no record
	// enters the queue, so the final drain sees every accepted record.
	mu     sync.RWMutex
	closed bool

	stop        chan struct{}
	done        chan struct{}
	stopOnce    sync.Once
	shutdownCtx context.Context
	// ctx is the context of every export; Shutdown cancels it when its
	// deadline passes so an export in flight cannot outlive it.
	ctx    context.Context
	cancel context.CancelFunc

	exported      atomic.Uint64
	failures      atomic.Uint64
	droppedFull   atomic.Uint64
	droppedFailed atomic.Uint64
	droppedStop   atomic.Uint64

	// lastWarning rate limits the failure warning (Unix nanoseconds).
	lastWarning atomic.Int64
	now         func() time.Time
	// closeIdle releases the client's kept-alive connections at shutdown.
	closeIdle func()
}

// warningInterval is the shortest gap between two export failure warnings.
const warningInterval = time.Minute

// New validates o, builds the OTLP/HTTP client and starts the batch loop.
// The client sends to the configured endpoint only: no proxy from the
// environment, no redirects, TLS 1.2 or later with the system roots, and
// the OTEL_EXPORTER_OTLP_* variables cannot change the endpoint, headers,
// certificates or timeout.
func New(o Options) (*Exporter, error) {
	if err := o.normalize(); err != nil {
		return nil, err
	}
	endpoint := o.Endpoint
	if u, err := url.Parse(endpoint); err == nil && (u.Path == "" || u.Path == "/") {
		u.Path = tracing.OTLPDefaultPath
		endpoint = u.String()
	}
	client := newHTTPClient(o.Timeout)
	headers := make(map[string]string, len(o.Headers))
	for name, value := range o.Headers {
		headers[name] = value
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	defer cancel()
	exporter, err := otlptrace.New(ctx, otlptracehttp.NewClient(
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithHTTPClient(client),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
		otlptracehttp.WithTimeout(o.Timeout),
		// Retries stay inside one export call and its timeout; a span
		// batch that still fails is dropped and counted.
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: true, InitialInterval: 500 * time.Millisecond, MaxInterval: 2 * time.Second, MaxElapsedTime: o.Timeout}),
	))
	if err != nil {
		client.CloseIdleConnections()
		return nil, errors.New("trace exporter could not be created")
	}
	e := start(o, exporter)
	e.closeIdle = client.CloseIdleConnections
	return e, nil
}

// newHTTPClient sends to the configured endpoint only: no proxy from the
// environment, no redirects (a collector that redirects would receive the
// headers' tokens at an address nobody configured), TLS 1.2 or later with
// the system roots.
func newHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func start(o Options, exporter sdktrace.SpanExporter) *Exporter {
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	e := &Exporter{queue: make(chan tracing.SpanRecord, o.QueueSize), exporter: exporter, convert: newConverter(),
		batchSize: o.BatchSize, interval: o.Interval, timeout: o.Timeout, logger: logger,
		stop: make(chan struct{}), done: make(chan struct{}), now: time.Now}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	go e.run()
	return e
}

// ExportSpan queues r without blocking; a full or stopped queue drops it.
func (e *Exporter) ExportSpan(r tracing.SpanRecord) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		e.droppedStop.Add(1)
		return
	}
	select {
	case e.queue <- r:
	default:
		e.droppedFull.Add(1)
	}
}

func (e *Exporter) run() {
	defer close(e.done)
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	batch := make([]tracing.SpanRecord, 0, e.batchSize)
	send := func(ctx context.Context) {
		if len(batch) > 0 {
			e.send(ctx, batch)
			clear(batch)
			batch = batch[:0]
		}
	}
	for {
		select {
		case r := <-e.queue:
			if batch = append(batch, r); len(batch) >= e.batchSize {
				send(e.ctx)
			}
		case <-ticker.C:
			send(e.ctx)
		case <-e.stop:
			// Shutdown closed the queue to new records before stop, so
			// this drain sees every record that was accepted.
			ctx := e.shutdownCtx
		drain:
			for {
				select {
				case r := <-e.queue:
					if batch = append(batch, r); len(batch) >= e.batchSize {
						send(ctx)
					}
				default:
					break drain
				}
			}
			send(ctx)
			return
		}
	}
}

// send exports one batch within the export timeout (and the shutdown
// deadline while stopping). A failed batch is dropped and counted.
func (e *Exporter) send(parent context.Context, batch []tracing.SpanRecord) {
	if parent.Err() != nil {
		e.droppedStop.Add(uint64(len(batch)))
		return
	}
	ctx, cancel := context.WithTimeout(parent, e.timeout)
	defer cancel()
	spans := make([]sdktrace.ReadOnlySpan, len(batch))
	for i := range batch {
		spans[i] = e.convert.span(batch[i])
	}
	if err := e.exporter.ExportSpans(ctx, spans); err != nil {
		e.failures.Add(1)
		if parent.Err() != nil {
			e.droppedStop.Add(uint64(len(batch)))
		} else {
			e.droppedFailed.Add(uint64(len(batch)))
		}
		e.warn(len(batch))
		return
	}
	e.exported.Add(uint64(len(batch)))
}

// warn logs at most one failure warning a minute. The error text is not
// logged: it can quote the endpoint and the collector's response body.
func (e *Exporter) warn(spans int) {
	now := e.now().UnixNano()
	last := e.lastWarning.Load()
	if last != 0 && now-last < int64(warningInterval) {
		return
	}
	if e.lastWarning.CompareAndSwap(last, now) {
		e.logger.Warn("trace export failed; spans dropped", "component", "system", "count", spans,
			"code", "trace_export_failed")
	}
}

// Shutdown stops accepting spans, exports what is queued within ctx and
// closes the client. When ctx ends first the export in flight is aborted
// and every span not delivered is counted as dropped before it returns
// ctx's error.
func (e *Exporter) Shutdown(ctx context.Context) error {
	e.stopOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.shutdownCtx = ctx
		e.mu.Unlock()
		close(e.stop)
	})
	var err error
	select {
	case <-e.done:
	case <-ctx.Done():
		// Abort the export in flight; the loop then counts what is left
		// as dropped and ends.
		err = ctx.Err()
		e.cancel()
		<-e.done
	}
	e.cancel()
	if shutdownErr := e.exporter.Shutdown(ctx); shutdownErr != nil && err == nil {
		err = errors.New("trace exporter shutdown failed")
	}
	if e.closeIdle != nil {
		e.closeIdle()
	}
	return err
}

// Exported reports the spans the collector accepted since start.
func (e *Exporter) Exported() uint64 { return e.exported.Load() }

// ExportFailures reports the export calls that failed since start.
func (e *Exporter) ExportFailures() uint64 { return e.failures.Load() }

// Dropped reports the spans lost for reason (one of DropReasons).
func (e *Exporter) Dropped(reason string) uint64 {
	switch reason {
	case DropQueueFull:
		return e.droppedFull.Load()
	case DropExportFailed:
		return e.droppedFailed.Load()
	case DropShutdown:
		return e.droppedStop.Load()
	}
	return 0
}
