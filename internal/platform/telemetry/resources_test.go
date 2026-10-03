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
		if len(families) != 30 {
			t.Fatalf("metric families: %d, want 30", len(families))
		}
		want := map[string]float64{"cpu_active": activeCPU, "io_active": activeIO, "total_active": total, "waiting": waiting, "cpu_limit": 1, "io_limit": 2, "total_limit": 2, "queue_limit": 1}
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
