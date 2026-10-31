package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing/otlpexport"
)

// BenchmarkBoundaryRequestTraceExport is BenchmarkBoundaryRequest with
// every request sampled and its root span handed to the OTLP exporter
// (G46.6): the request path pays the span record and one queue send, the
// export itself runs on the exporter's goroutine against a local collector.
func BenchmarkBoundaryRequestTraceExport(b *testing.B) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	router, err := logging.Open(logging.Options{BufferEntries: 1 << 16}, io.Discard)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = router.Close() })
	exporter, err := otlpexport.New(otlpexport.Options{OTLPSettings: tracing.OTLPSettings{Endpoint: collector.URL, Insecure: true}})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = exporter.Shutdown(context.Background()) }()
	tracer, err := tracing.New(tracing.Options{SampleRate: 1, Logger: router.Logger(), Exporter: exporter})
	if err != nil {
		b.Fatal(err)
	}
	previous := tracing.SetDefault(tracer)
	defer tracing.SetDefault(previous)
	handler, err := New(validConfig(), &fakeBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, router.Logger())
	if err != nil {
		b.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
	w := &benchResponseWriter{}
	b.ReportAllocs()
	for b.Loop() {
		w.header = http.Header{}
		handler.ServeHTTP(w, request)
	}
	b.StopTimer()
	if exporter.Dropped(otlpexport.DropExportFailed) != 0 {
		b.Fatal("local collector refused spans")
	}
}
