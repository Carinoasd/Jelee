package logging_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	_ "github.com/MoYuanCN/Jelee/internal/platform/logging/otlplog"
)

// The sensitive samples of the G46.5 scan must not reach an OTLP collector
// either: the decompressed protobuf requests hold none of the needles. The
// strings of an OTLP request are stored as plain UTF-8, so a byte search
// covers bodies, attribute keys and values alike.
func TestSensitiveSamplesDoNotReachOTLPCollector(t *testing.T) {
	var mu sync.Mutex
	var requests bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(zr)
		mu.Lock()
		requests.Write(body)
		requests.WriteByte('\n')
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	router, err := logging.Open(logging.Options{Level: -4, BufferEntries: 1 << 16, Forward: []logging.ForwarderOptions{
		{Kind: logging.ForwardOTLP, QueueEntries: 1 << 16, BatchSize: 1000, OTLP: logging.OTLPOptions{Endpoint: srv.URL, Insecure: true}},
	}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	samples := logging.SensitiveSamples()
	for _, sample := range samples {
		logging.InjectSensitive(router.Logger(), sample.Value)
	}
	if err := router.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	for _, st := range router.ForwardStats() {
		if st.Dropped != 0 || st.Sent == 0 {
			t.Fatalf("stats %+v", st)
		}
	}
	mu.Lock()
	output := requests.String()
	mu.Unlock()
	if !strings.Contains(output, "jelee") {
		t.Fatal("the collector received no request")
	}
	for _, sample := range samples {
		logging.AssertNoLeak(t, "otlp", output, sample.Needles)
	}
}
