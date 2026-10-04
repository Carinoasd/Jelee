package telemetry

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func TestResourceMetricsSaturatedQueueAndCancellation(t *testing.T) {
	b, err := resources.New(resources.Limits{CPU: 1, IO: 2, Total: 2, Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	source := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) { return jobMetricsTestSnapshot(1), nil }}
	m, err := NewWithResources(&testPool{value: samplePool()}, source, b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	cpu, err := b.Acquire(context.Background(), app.WorkCPU)
	if err != nil {
		t.Fatal(err)
	}
	defer cpu()
	io, err := b.Acquire(context.Background(), app.WorkIO)
	if err != nil {
		t.Fatal(err)
	}
	defer io()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		release, err := b.Acquire(ctx, app.WorkIO)
		if release != nil {
			release()
		}
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for b.Stats().Waiting != 1 {
		if time.Now().After(deadline) {
			t.Fatal("waiter did not enter queue")
		}
		time.Sleep(time.Millisecond)
	}
	assert := func(activeCPU, activeIO, total, waiting float64) {
		t.Helper()
		w := requestJobMetrics(m, context.Background())
		if w.Code != http.StatusOK || w.Body.Len() > 64*1024 {
			t.Fatalf("resource scrape status/size: %d/%d", w.Code, w.Body.Len())
		}
		parser := expfmt.NewTextParser(model.LegacyValidation)
		families, err := parser.TextToMetricFamilies(strings.NewReader(w.Body.String()))
		if err != nil {
			t.Fatal(err)
		}
		if len(families) != 34 {
			t.Fatalf("metric families: %d, want 34", len(families))
		}
		want := map[string]float64{"cpu_active": activeCPU, "io_active": activeIO, "total_active": total, "waiting": waiting, "cpu_limit": 1, "io_limit": 2, "total_limit": 2, "queue_limit": 1,
			"cpu_effective": 1, "io_effective": 2, "total_effective": 2}
		for suffix, value := range want {
			family := families["jelee_resources_"+suffix]
			if family == nil || len(family.Metric) != 1 {
				t.Fatalf("missing or duplicated resource metric %s", suffix)
			}
			sample := family.Metric[0]
			if sample.Gauge == nil || len(sample.Label) != 0 || sample.GetGauge().GetValue() != value {
				t.Fatalf("resource metric %s: %v, want %v", suffix, sample, value)
			}
		}
	}
	assert(1, 1, 2, 1)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter did not cancel")
	}
	assert(1, 1, 2, 0)
	cpu()
	io()
	assert(0, 0, 0, 0)
}

func TestResourceMetricsRequireBudget(t *testing.T) {
	if _, err := NewWithResources(&testPool{value: samplePool()}, nil, nil); err == nil {
		t.Fatal("nil budget accepted")
	}
}

// TestAdaptiveResourceMetrics: the effective ceilings and the pressure
// source that lowered them are exported next to the configured limits.
func TestAdaptiveResourceMetrics(t *testing.T) {
	b, err := resources.New(resources.Limits{CPU: 8, IO: 16, Total: 32, Queue: 4})
	if err != nil {
		t.Fatal(err)
	}
	source := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) { return jobMetricsTestSnapshot(1), nil }}
	m, err := NewWithResources(&testPool{value: samplePool()}, source, b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	scrape := func() map[string]float64 {
		t.Helper()
		w := requestJobMetrics(m, context.Background())
		parser := expfmt.NewTextParser(model.LegacyValidation)
		families, err := parser.TextToMetricFamilies(strings.NewReader(w.Body.String()))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, name := range []string{"cpu_effective", "io_effective", "total_effective", "cpu_limit"} {
			out[name] = families["jelee_resources_"+name].Metric[0].GetGauge().GetValue()
		}
		for _, sample := range families["jelee_resources_adaptive_pressure"].Metric {
			out["pressure_"+sample.Label[0].GetValue()] = sample.GetGauge().GetValue()
		}
		return out
	}
	got := scrape()
	if got["cpu_effective"] != 8 || got["io_effective"] != 16 || got["total_effective"] != 32 || got["pressure_load"] != 0 || got["pressure_memory_pressure"] != 0 {
		t.Fatalf("unadjusted metrics: %v", got)
	}
	b.SetEffective(resources.Limits{CPU: 4, IO: 8, Total: 16}, resources.PressureMemoryPressure)
	got = scrape()
	if got["cpu_effective"] != 4 || got["io_effective"] != 8 || got["total_effective"] != 16 || got["cpu_limit"] != 8 || got["pressure_memory_pressure"] != 1 || got["pressure_load"] != 0 {
		t.Fatalf("lowered metrics: %v", got)
	}
}
