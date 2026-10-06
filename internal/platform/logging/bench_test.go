package logging

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Log write benchmarks (G46.8): the cost a request path pays per record
// through the router (whitelist, level filter, asynchronous queue). The
// sink is io.Discard so the numbers measure the caller's side, not disk.
// make bench runs them and tools/benchgate gates regressions.

func benchRouter(b *testing.B, opts Options) *Router {
	b.Helper()
	opts.BufferEntries = 1 << 16
	r, err := Open(opts, io.Discard)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = r.Close() })
	return r
}

// BenchmarkLogAccessRecord writes the access log record of one request.
func BenchmarkLogAccessRecord(b *testing.B) {
	RegisterRoutePatterns("/api/v1/items/{id}")
	log := benchRouter(b, Options{}).Logger()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		log.InfoContext(ctx, "request completed", "component", "http", "requestId", "5f0c6a3b9d2e4f718a6b5c4d3e2f1a0b", "method", "GET", "route", "/api/v1/items/{id}",
			"status", 200, "durationMs", int64(3), "bytes", int64(512), "media", false, "userId", "00000000-0000-4000-8000-000000000001", "clientId", "00000000-0000-4000-8000-000000000002")
	}
}

// BenchmarkLogContextFields writes a record whose identity comes from the
// context (G46.4), as handlers and workers do.
func BenchmarkLogContextFields(b *testing.B) {
	log := benchRouter(b, Options{}).Logger()
	ctx := domain.WithLogFields(context.Background(), domain.LogFields{UserID: "00000000-0000-4000-8000-000000000001", ClientID: "00000000-0000-4000-8000-000000000002",
		ItemID: "00000000-0000-4000-8000-000000000003"})
	b.ReportAllocs()
	for b.Loop() {
		log.InfoContext(ctx, "item metadata changed", "component", "http", "count", 1)
	}
}

// BenchmarkLogFilteredDebug is a DEBUG record below the threshold, the
// common case of verbose call sites in production.
func BenchmarkLogFilteredDebug(b *testing.B) {
	log := benchRouter(b, Options{}).Logger()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		log.DebugContext(ctx, "scan entry", "component", "scan", "count", 1)
	}
}

// BenchmarkLogParallel writes from every CPU at once (high QPS).
func BenchmarkLogParallel(b *testing.B) {
	log := benchRouter(b, Options{Level: slog.LevelInfo}).Logger()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			log.InfoContext(ctx, "request completed", "component", "http", "method", "GET", "status", 200, "durationMs", int64(1))
		}
	})
}
