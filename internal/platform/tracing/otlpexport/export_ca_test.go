package otlpexport

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/tlstrust"
	"github.com/MoYuanCN/Jelee/internal/platform/tlstrust/tlstrusttest"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
)

// A collector behind a private CA receives spans once its CA file is
// configured; another CA file, or the system roots, refuse its certificate.
func TestCAFileTrustsOnlyItsCertificates(t *testing.T) {
	ca := tlstrusttest.NewCA(t, "collector ca")
	var requests atomic.Int32
	srv := ca.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	export := func(caFile string) *Exporter {
		e := newTestExporter(t, srv.URL, func(o *Options) {
			o.Insecure, o.CAFile, o.BatchSize, o.Timeout = false, caFile, 1, time.Second
		})
		tracer := newTracer(t, 1, e, io.Discard)
		_, span := tracer.StartRequest(context.Background(), "", "http.request", "http")
		span.End("ok")
		_ = e.Shutdown(context.Background())
		return e
	}
	if e := export(ca.WriteFile(t)); e.Exported() != 1 || e.ExportFailures() != 0 || requests.Load() != 1 {
		t.Fatalf("exported %d failures %d requests %d", e.Exported(), e.ExportFailures(), requests.Load())
	}
	for name, caFile := range map[string]string{"other CA": tlstrusttest.NewCA(t, "other").WriteFile(t), "system roots": ""} {
		before := requests.Load()
		if e := export(caFile); e.Exported() != 0 || e.ExportFailures() == 0 || requests.Load() != before {
			t.Fatalf("%s: certificate of the private CA accepted: exported %d failures %d", name, e.Exported(), e.ExportFailures())
		}
	}
}

func TestCAFileSettings(t *testing.T) {
	ca := tlstrusttest.NewCA(t, "ca")
	caFile := ca.WriteFile(t)
	notPEM := filepath.Join(t.TempDir(), "not.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]tracing.OTLPSettings{
		"insecure http": {Endpoint: "http://127.0.0.1:4318", Insecure: true, CAFile: caFile},
		"missing":       {Endpoint: "https://collector.example", CAFile: filepath.Join(t.TempDir(), "missing.pem")},
		"not pem":       {Endpoint: "https://collector.example", CAFile: notPEM},
		"relative":      {Endpoint: "https://collector.example", CAFile: "ca.pem"},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		} else if strings.Contains(err.Error(), string(os.PathSeparator)) || strings.Contains(err.Error(), "example") {
			t.Errorf("%s: error echoes a setting: %v", name, err)
		}
		if e, err := New(Options{OTLPSettings: s}); err == nil {
			_ = e.Shutdown(context.Background())
			t.Errorf("%s: exporter built", name)
		}
	}
	pool, err := tlstrust.LoadPool(caFile)
	if err != nil {
		t.Fatal(err)
	}
	transport := newHTTPClient(time.Second, pool).Transport.(*http.Transport)
	if c := transport.TLSClientConfig; c.RootCAs != pool || c.InsecureSkipVerify || c.MinVersion < tls.VersionTLS12 {
		t.Fatalf("CA file client %+v", c)
	}
}
