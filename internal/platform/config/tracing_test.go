package config

import (
	"strings"
	"testing"
	"time"
)

func loadTracing(t *testing.T, env map[string]string) (Config, error) {
	t.Helper()
	return LoadWith(func(k string) (string, bool) {
		if k == "JELEE_DATABASE_URL" {
			return "postgres://jelee@localhost/jelee", true
		}
		v, ok := env[k]
		return v, ok
	})
}

func TestTracingExportIsOffByDefault(t *testing.T) {
	c, err := loadTracing(t, nil)
	if err != nil || c.Tracing.Enabled() || c.Tracing.Exporter != "" {
		t.Fatalf("default tracing %+v %v", c.Tracing, err)
	}
	if (TracingConfig{Exporter: TraceExporterNone}).Validate() != nil {
		t.Fatal("explicit none refused")
	}
}

func TestTracingEnvironment(t *testing.T) {
	c, err := loadTracing(t, map[string]string{
		"JELEE_TRACE_EXPORTER":               "otlp",
		"JELEE_TRACE_OTLP_ENDPOINT":          "https://collector.internal:4318/v1/traces",
		"JELEE_TRACE_OTLP_HEADERS":           "Authorization=Bearer%20abc, X-Scope-OrgID=jelee ,",
		"JELEE_TRACE_OTLP_TIMEOUT_SECONDS":   "3",
		"JELEE_TRACE_OTLP_BATCH_SIZE":        "100",
		"JELEE_TRACE_OTLP_BATCH_INTERVAL_MS": "250",
		"JELEE_TRACE_OTLP_QUEUE_SIZE":        "1000",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := c.Tracing.OTLP.Settings()
	if !c.Tracing.Enabled() || s.Endpoint != "https://collector.internal:4318/v1/traces" || s.Headers["Authorization"] != "Bearer abc" || s.Headers["X-Scope-OrgID"] != "jelee" ||
		s.Timeout != 3*time.Second || s.BatchSize != 100 || s.Interval != 250*time.Millisecond || s.QueueSize != 1000 || s.Insecure {
		t.Fatalf("settings %+v", s)
	}
	local, err := loadTracing(t, map[string]string{"JELEE_TRACE_EXPORTER": "otlp", "JELEE_TRACE_OTLP_ENDPOINT": "http://127.0.0.1:4318", "JELEE_TRACE_OTLP_INSECURE": "true"})
	if err != nil || !local.Tracing.OTLP.Insecure {
		t.Fatalf("loopback http %+v %v", local.Tracing, err)
	}
	for key, value := range map[string]string{"JELEE_TRACE_OTLP_INSECURE": "maybe", "JELEE_TRACE_OTLP_TIMEOUT_SECONDS": "x", "JELEE_TRACE_OTLP_BATCH_SIZE": "x",
		"JELEE_TRACE_OTLP_BATCH_INTERVAL_MS": "x", "JELEE_TRACE_OTLP_QUEUE_SIZE": "x", "JELEE_TRACE_OTLP_HEADERS": "no-equals"} {
		if _, err := loadTracing(t, map[string]string{"JELEE_TRACE_EXPORTER": "otlp", "JELEE_TRACE_OTLP_ENDPOINT": "https://c.example", key: value}); err == nil {
			t.Fatalf("invalid %s accepted", key)
		}
	}
	if _, err := loadTracing(t, map[string]string{"JELEE_TRACE_EXPORTER": "otlp", "JELEE_TRACE_OTLP_ENDPOINT": "https://c.example", "JELEE_TRACE_OTLP_HEADERS": "A=%zz"}); err == nil {
		t.Fatal("undecodable header accepted")
	}
	if c, err := loadTracing(t, map[string]string{"JELEE_TRACE_EXPORTER": "otlp", "JELEE_TRACE_OTLP_ENDPOINT": "https://c.example", "JELEE_TRACE_OTLP_HEADERS": " , "}); err != nil || c.Tracing.OTLP.Headers != nil {
		t.Fatalf("empty header list %+v %v", c.Tracing.OTLP.Headers, err)
	}
}

func TestTracingHalfConfigurationRefused(t *testing.T) {
	for name, c := range map[string]TracingConfig{
		"unknown exporter":       {Exporter: "jaeger"},
		"otlp without endpoint":  {Exporter: TraceExporterOTLP},
		"endpoint without otlp":  {OTLP: OTLPTraceConfig{Endpoint: "https://c.example"}},
		"headers without otlp":   {Exporter: TraceExporterNone, OTLP: OTLPTraceConfig{Headers: map[string]string{"Authorization": "Bearer secret-value"}}},
		"remote plain http":      {Exporter: TraceExporterOTLP, OTLP: OTLPTraceConfig{Endpoint: "http://secret-host.example:4318", Insecure: true}},
		"http without insecure":  {Exporter: TraceExporterOTLP, OTLP: OTLPTraceConfig{Endpoint: "http://127.0.0.1:4318"}},
		"timeout out of range":   {Exporter: TraceExporterOTLP, OTLP: OTLPTraceConfig{Endpoint: "https://c.example", TimeoutSeconds: 61}},
		"negative timeout":       {Exporter: TraceExporterOTLP, OTLP: OTLPTraceConfig{Endpoint: "https://c.example", TimeoutSeconds: -1}},
		"interval out of range":  {Exporter: TraceExporterOTLP, OTLP: OTLPTraceConfig{Endpoint: "https://c.example", BatchIntervalMs: 50}},
		"batch out of range":     {Exporter: TraceExporterOTLP, OTLP: OTLPTraceConfig{Endpoint: "https://c.example", BatchSize: 5000}},
		"queue out of range":     {Exporter: TraceExporterOTLP, OTLP: OTLPTraceConfig{Endpoint: "https://c.example", QueueSize: 1 << 17}},
		"header with line break": {Exporter: TraceExporterOTLP, OTLP: OTLPTraceConfig{Endpoint: "https://c.example", Headers: map[string]string{"Authorization": "Bearer secret-value\r\nX: 1"}}},
	} {
		err := c.Validate()
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s: error echoes a configured value: %v", name, err)
		}
	}
	ok := TracingConfig{Exporter: TraceExporterOTLP, OTLP: OTLPTraceConfig{Endpoint: "https://c.example", TimeoutSeconds: 60, BatchIntervalMs: 100, BatchSize: 4096, QueueSize: 1 << 16}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("bounds refused: %v", err)
	}
}
