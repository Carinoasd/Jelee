package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// BenchmarkBoundaryRequest serves GET /healthz through the request boundary
// with the production log router (G46.8): request ID, span, access record
// with the route pattern and status, and the asynchronous log queue. It is
// the per-request logging cost a high request rate pays.
func BenchmarkBoundaryRequest(b *testing.B) {
	router, err := logging.Open(logging.Options{BufferEntries: 1 << 16}, io.Discard)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = router.Close() })
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
}
