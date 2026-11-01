package telemetry

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// TestResourceWaitHistogramExposition: the shared budget's wait
// distribution is one histogram family with exactly the class and outcome
// labels (four series, zero series included), 1 ms to 60 s buckets, and
// counts that follow the budget.
func TestResourceWaitHistogramExposition(t *testing.T) {
	b, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 2, Queue: 1})
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
	scrape := func() *dto.MetricFamily {
		t.Helper()
		w := requestJobMetrics(m, context.Background())
		if w.Code != http.StatusOK {
			t.Fatalf("scrape status %d", w.Code)
		}
		parser := expfmt.NewTextParser(model.LegacyValidation)
		families, err := parser.TextToMetricFamilies(strings.NewReader(w.Body.String()))
		if err != nil {
			t.Fatal(err)
		}
		family := families["jelee_resources_wait_seconds"]
		if family == nil || family.GetType() != dto.MetricType_HISTOGRAM || len(family.Metric) != 4 {
			t.Fatalf("wait family: %v", family)
		}
		return family
	}
	point := func(family *dto.MetricFamily, class, outcome string) *dto.Histogram {
		t.Helper()
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, l := range metric.Label {
				labels[l.GetName()] = l.GetValue()
			}
			if len(labels) != 2 {
				t.Fatalf("wait series labels: %v", labels)
			}
			if labels["class"] == class && labels["outcome"] == outcome {
				return metric.Histogram
			}
		}
		t.Fatalf("no wait series %s/%s", class, outcome)
		return nil
	}
	h := point(scrape(), "io", resources.WaitGranted)
	if h.GetSampleCount() != 0 || len(h.Bucket) != 15 || h.Bucket[0].GetUpperBound() != 0.001 || h.Bucket[13].GetUpperBound() != 60 || !math.IsInf(h.Bucket[14].GetUpperBound(), 1) {
		t.Fatalf("empty histogram: %v", h)
	}
	cpu, err := b.Acquire(context.Background(), app.WorkCPU)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		release, err := b.Acquire(ctx, app.WorkCPU)
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
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cpu()
	family := scrape()
	if granted := point(family, "cpu", resources.WaitGranted); granted.GetSampleCount() != 1 || granted.Bucket[0].GetCumulativeCount() != 1 {
		t.Fatalf("cpu granted: %v", granted)
	}
	cancelled := point(family, "cpu", resources.WaitCancelled)
	if cancelled.GetSampleCount() != 1 || cancelled.GetSampleSum() < 0 || math.IsNaN(cancelled.GetSampleSum()) || cancelled.Bucket[13].GetCumulativeCount() != 1 {
		t.Fatalf("cpu cancelled: %v", cancelled)
	}
	if io := point(family, "io", resources.WaitGranted); io.GetSampleCount() != 0 {
		t.Fatalf("io granted: %v", io)
	}
}

// Without NewWithResources no wait family is exposed.
func TestResourceWaitHistogramNeedsBudget(t *testing.T) {
	source := &jobMetricsTestSource{read: func(context.Context) (app.JobMetricsSnapshot, error) { return jobMetricsTestSnapshot(1), nil }}
	m, err := NewWithJobs(&testPool{value: samplePool()}, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	w := requestJobMetrics(m, context.Background())
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "jelee_resources_wait") {
		t.Fatalf("wait family without budget: %d", w.Code)
	}
}
