package otlpexport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/testutil"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
)

// G29.3: Shutdown stops the batch loop and closes the exporter's client
// connections; nothing it started keeps running.
func TestShutdownLeavesNoGoroutines(t *testing.T) {
	testutil.TrackGoroutines(t)
	recv := newReceiver(t)
	e := newTestExporter(t, recv.server.URL, func(o *Options) { o.BatchSize = 4 })
	for range 10 {
		e.ExportSpan(record())
	}
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(recv.received()); got != 10 || e.Exported() != 10 {
		t.Fatalf("exported %d spans, counter %d", got, e.Exported())
	}
}

// A shutdown deadline that ends while the collector hangs on an export
// aborts it; the batch loop and the client connection are gone when
// Shutdown returns.
func TestShutdownDeadlineWithHungCollectorLeavesNoGoroutines(t *testing.T) {
	hang := make(chan struct{})
	entered := make(chan struct{}, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-hang
	}))
	defer collector.Close()
	defer close(hang)
	// The collector's own connection goroutines are the fake's.
	baseline := testutil.SnapshotGoroutines("net/http.(*conn).serve", "net/http.(*connReader).backgroundRead")
	e, err := New(Options{OTLPSettings: tracing.OTLPSettings{Endpoint: collector.URL, Insecure: true, BatchSize: 1, Interval: time.Minute, Timeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	e.ExportSpan(record())
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := e.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error %v", err)
	}
	if waited := time.Since(started); waited > 5*time.Second {
		t.Fatalf("shutdown waited %v for the hung collector", waited)
	}
	baseline.Check(t)
}

// The batch loop over a blocked exporter also ends at the shutdown
// deadline.
func TestShutdownDeadlineWithBlockedExporterLeavesNoGoroutines(t *testing.T) {
	testutil.TrackGoroutines(t)
	block := &blockingExporter{release: make(chan struct{})}
	e := start(Options{OTLPSettings: tracing.OTLPSettings{Timeout: time.Minute, BatchSize: 1, Interval: time.Hour, QueueSize: 16}}, block)
	for range 3 {
		e.ExportSpan(record())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := e.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error %v", err)
	}
}
