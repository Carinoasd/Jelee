package telemetry

import (
	"context"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/cache"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func TestCacheMetricsPerNamedCache(t *testing.T) {
	registry := cache.NewRegistry()
	specs, err := cache.New(cache.Options[string, []byte]{MaxEntries: 2, MaxBytes: 100,
		Size: func(_ string, v []byte) int64 { return int64(len(v)) }})
	if err != nil {
		t.Fatal(err)
	}
	idle, err := cache.New(cache.Options[int, int]{MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("http.openapi", specs); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("idle", idle); err != nil {
		t.Fatal(err)
	}
	specs.Set("a", []byte("12345"))
	specs.Get("a")
	specs.Get("a")
	specs.Get("a")
	specs.Get("missing")
	specs.Set("b", []byte("1"))
	specs.Set("c", []byte("12")) // evicts a

	m := testMetrics(t, &testPool{value: samplePool()}, sampleRuntime)
	if err := m.RegisterCaches(registry); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterCaches(nil); err == nil {
		t.Fatal("nil cache source accepted")
	}
	w := requestJobMetrics(m, context.Background())
	if w.Code != 200 {
		t.Fatalf("scrape status %d", w.Code)
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(w.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		typ          dto.MetricType
		openapi, idl float64
	}{
		"jelee_cache_hits_total":        {dto.MetricType_COUNTER, 3, 0},
		"jelee_cache_misses_total":      {dto.MetricType_COUNTER, 1, 0},
		"jelee_cache_evictions_total":   {dto.MetricType_COUNTER, 1, 0},
		"jelee_cache_expirations_total": {dto.MetricType_COUNTER, 0, 0},
		"jelee_cache_entries":           {dto.MetricType_GAUGE, 2, 0},
		"jelee_cache_size_bytes":        {dto.MetricType_GAUGE, 3, 0},
		"jelee_cache_hit_ratio":         {dto.MetricType_GAUGE, 0.75, 0},
	}
	for name, w := range want {
		family := families[name]
		if family == nil || family.GetType() != w.typ || len(family.Metric) != 2 {
			t.Fatalf("family %s: %v", name, family)
		}
		for _, point := range family.Metric {
			if len(point.Label) != 1 || point.Label[0].GetName() != "cache" {
				t.Fatalf("%s labels %v", name, point.Label)
			}
			value := point.GetGauge().GetValue()
			if w.typ == dto.MetricType_COUNTER {
				value = point.GetCounter().GetValue()
			}
			expected := map[string]float64{"http.openapi": w.openapi, "idle": w.idl}[point.Label[0].GetValue()]
			if value != expected {
				t.Fatalf("%s{%s} = %v, want %v", name, point.Label[0].GetValue(), value, expected)
			}
		}
	}
}
