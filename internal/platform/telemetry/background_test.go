package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	dto "github.com/prometheus/client_model/go"
)

type backgroundSource struct {
	snapshot app.BackgroundJobMetricsSnapshot
}

func (s *backgroundSource) BackgroundJobMetrics() app.BackgroundJobMetricsSnapshot { return s.snapshot }

func sampleBackground() app.BackgroundJobMetricsSnapshot {
	s := app.BackgroundJobMetricsSnapshot{StartedAt: time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)}
	for i, kind := range domain.BackgroundJobKinds() {
		s.Groups[i].Kind = kind
	}
	// webhook_delivery: 5 runs started (one still running), 4 ended:
	// 3 succeeded, 1 failed; 2 skipped and 1 cancelled before it started.
	g := &s.Groups[domain.BackgroundJobKindIndex(domain.JobWebhookDelivery)]
	g.Running = 1
	g.Outcomes = [4]uint64{3, 1, 1, 2}
	g.Wait = app.JobMetricHistogram{Count: 5, SumSeconds: 1.5, BucketCounts: [13]uint64{4, 0, 1}}
	g.Duration = app.JobMetricHistogram{Count: 4, SumSeconds: 40, BucketCounts: [13]uint64{2, 0, 0, 0, 0, 1, 0, 1}}
	return s
}

func TestBackgroundJobMetrics(t *testing.T) {
	source := &imageTestSource{}
	source.set(1)
	m := newImageTestExporter(t, source)
	if families := scrapeOps(t, m); families["jelee_jobs_background_runs_total"] != nil {
		t.Fatal("background families exposed before registration")
	}
	background := &backgroundSource{snapshot: sampleBackground()}
	if err := m.RegisterBackgroundJobs(nil); err == nil {
		t.Fatal("nil source accepted")
	}
	if err := m.RegisterBackgroundJobs(background); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterBackgroundJobs(background); err == nil {
		t.Fatal("second source accepted")
	}
	families := scrapeOps(t, m)
	kinds, outcomes := len(app.BackgroundJobMetricsSnapshot{}.Groups), len(app.BackgroundJobGroup{}.Outcomes)
	for _, spec := range []struct {
		name   string
		typ    dto.MetricType
		points int
	}{
		{"jelee_jobs_background_running", dto.MetricType_GAUGE, kinds},
		{"jelee_jobs_background_runs_total", dto.MetricType_COUNTER, kinds * outcomes},
		{"jelee_jobs_background_wait_seconds", dto.MetricType_HISTOGRAM, kinds},
		{"jelee_jobs_background_duration_seconds", dto.MetricType_HISTOGRAM, kinds},
	} {
		family := families[spec.name]
		if family == nil || family.GetType() != spec.typ || len(family.Metric) != spec.points {
			t.Fatalf("%s: %v", spec.name, family)
		}
		for _, point := range family.Metric {
			for _, label := range point.Label {
				switch label.GetName() {
				case "kind", "outcome":
				case "priority":
					if label.GetValue() != domain.BackgroundJobPriority {
						t.Fatalf("%s priority %q", spec.name, label.GetValue())
					}
				default:
					t.Fatalf("%s has label %s", spec.name, label.GetName())
				}
			}
		}
	}
	labels := func(kind string, outcome ...string) map[string]string {
		l := map[string]string{"kind": kind, "priority": "background"}
		if len(outcome) > 0 {
			l["outcome"] = outcome[0]
		}
		return l
	}
	for outcome, want := range map[string]float64{"succeeded": 3, "failed": 1, "cancelled": 1, "skipped": 2} {
		if got := opsValue(t, families, "jelee_jobs_background_runs_total", labels(domain.JobWebhookDelivery, outcome)); got != want {
			t.Errorf("webhook %s = %v, want %v", outcome, got, want)
		}
		if got := opsValue(t, families, "jelee_jobs_background_runs_total", labels(domain.JobPlaybackProgressFlush, outcome)); got != 0 {
			t.Errorf("idle flush %s = %v", outcome, got)
		}
	}
	if got := opsValue(t, families, "jelee_jobs_background_running", labels(domain.JobWebhookDelivery)); got != 1 {
		t.Errorf("running %v", got)
	}
	for _, point := range families["jelee_jobs_background_duration_seconds"].Metric {
		h := point.GetHistogram()
		kind := ""
		for _, label := range point.Label {
			if label.GetName() == "kind" {
				kind = label.GetValue()
			}
		}
		if len(h.Bucket) != 13 || h.Bucket[0].GetUpperBound() != 0.1 {
			t.Fatalf("buckets %v", h.Bucket)
		}
		if kind == domain.JobWebhookDelivery && (h.GetSampleCount() != 4 || h.GetSampleSum() != 40 || h.Bucket[0].GetCumulativeCount() != 2 || h.Bucket[5].GetCumulativeCount() != 3 || h.Bucket[7].GetCumulativeCount() != 4) {
			t.Fatalf("webhook duration %v", h)
		}
	}
	// An inconsistent snapshot fails the scrape instead of exposing it.
	broken := sampleBackground()
	broken.Groups[0].Running = -1
	if validBackgroundSnapshot(broken) {
		t.Fatal("negative running accepted")
	}
	broken = sampleBackground()
	broken.Groups[2].Wait.Count++
	if validBackgroundSnapshot(broken) {
		t.Fatal("a wait sample without a run accepted")
	}
	broken = sampleBackground()
	broken.Groups[1].Kind = domain.JobWebhookDelivery
	if validBackgroundSnapshot(broken) {
		t.Fatal("kinds out of order accepted")
	}
	if _, err := (&backgroundProducer{}).Produce(context.Background()); err != nil {
		t.Fatalf("unregistered producer: %v", err)
	}
	background.snapshot = broken
	p := &backgroundProducer{}
	p.source.Store(&backgroundSourceHolder{source: background})
	if _, err := p.Produce(context.Background()); err == nil {
		t.Fatal("invalid snapshot produced")
	}
}
