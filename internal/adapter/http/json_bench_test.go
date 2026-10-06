package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// benchResponseWriter discards the body and reuses one header map so the
// benchmark counts writeJSON's own encoding cost, not a recorder's buffer.
type benchResponseWriter struct {
	header http.Header
	n      int
}

func (w *benchResponseWriter) Header() http.Header { return w.header }
func (w *benchResponseWriter) WriteHeader(int)     {}
func (w *benchResponseWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

// BenchmarkWriteJSONItemPage encodes the catalog list envelope for a full
// 50-item page of synthetic items, matching Server.list's response shape.
func BenchmarkWriteJSONItemPage(b *testing.B) {
	items := make([]domain.Item, 50)
	for i := range items {
		items[i] = domain.Item{
			ID:        fmt.Sprintf("00000000-0000-4000-8000-%012d", i),
			LibraryID: "11111111-1111-4111-8111-111111111111",
			Title:     fmt.Sprintf("Synthetic Title %02d — 合成标题", i),
			Kind:      "movie",
		}
		if i%3 == 0 {
			items[i].ParentID = "22222222-2222-4222-8222-222222222222"
		}
	}
	w := &benchResponseWriter{header: http.Header{}}
	b.ReportAllocs()
	for b.Loop() {
		w.n = 0
		writeJSON(w, 200, map[string]any{"data": items, "pagination": map[string]any{"nextCursor": items[len(items)-1].ID, "limit": len(items)}})
		if w.n == 0 {
			b.Fatal("empty body")
		}
	}
}

// BenchmarkWriteJSONError encodes the uniform error envelope written by
// WriteError, the most frequent small response on rejected requests.
func BenchmarkWriteJSONError(b *testing.B) {
	w := &benchResponseWriter{header: http.Header{"X-Request-Id": {"0123456789abcdef"}}}
	b.ReportAllocs()
	for b.Loop() {
		w.n = 0
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "Resource not found", "details": map[string]any{}, "traceId": w.Header().Get("X-Request-ID")}})
		if w.n == 0 {
			b.Fatal("empty body")
		}
	}
}

// benchItemPage is the 50-item list envelope of BenchmarkWriteJSONItemPage.
func benchItemPage() map[string]any {
	items := make([]domain.Item, 50)
	for i := range items {
		items[i] = domain.Item{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), LibraryID: "11111111-1111-4111-8111-111111111111", Title: fmt.Sprintf("Synthetic Title %02d — 合成标题", i), Kind: "movie"}
	}
	return map[string]any{"data": items, "pagination": map[string]any{"nextCursor": items[len(items)-1].ID, "limit": len(items)}}
}

// BenchmarkWriteJSONItemPageGzip is the same page through the G11.7
// compression writer at the default level, for a client accepting gzip.
func BenchmarkWriteJSONItemPageGzip(b *testing.B) {
	z := newCompression(config.CompressionConfig{})
	page := benchItemPage()
	r, _ := http.NewRequest(http.MethodGet, "http://localhost/api/v1/items", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := &benchResponseWriter{header: http.Header{}}
	b.ReportAllocs()
	for b.Loop() {
		w.n = 0
		clear(w.header)
		wrapped, finish := z.wrap(w, r)
		writeJSON(wrapped, 200, page)
		finish()
		if w.n == 0 {
			b.Fatal("empty body")
		}
	}
}

// BenchmarkWriteJSONErrorCompressionPassthrough is the small error envelope
// through the compression writer: below the threshold it is buffered and
// written unencoded, the overhead every small response pays.
func BenchmarkWriteJSONErrorCompressionPassthrough(b *testing.B) {
	z := newCompression(config.CompressionConfig{})
	r, _ := http.NewRequest(http.MethodGet, "http://localhost/api/v1/items/x", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := &benchResponseWriter{header: http.Header{}}
	b.ReportAllocs()
	for b.Loop() {
		w.n = 0
		clear(w.header)
		wrapped, finish := z.wrap(w, r)
		writeJSON(wrapped, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "Resource not found", "details": map[string]any{}, "traceId": "0123456789abcdef"}})
		finish()
		if w.n == 0 {
			b.Fatal("empty body")
		}
	}
}
