package telemetry

import (
	"context"
	"errors"
	"math"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/cache"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type opsTestSource struct {
	snapshot app.OpsMetricsSnapshot
	err      error
	calls    atomic.Int64
	wait     bool
}

func (s *opsTestSource) OpsMetrics(ctx context.Context) (app.OpsMetricsSnapshot, error) {
	s.calls.Add(1)
	if s.wait {
		<-ctx.Done()
		return app.OpsMetricsSnapshot{}, ctx.Err()
	}
	return s.snapshot, s.err
}

type blockedTestSource struct{ n atomic.Int64 }

func (s *blockedTestSource) BlockedTotal() int64 { return s.n.Load() }

func opsTestSnapshot() app.OpsMetricsSnapshot {
	s := app.OpsMetricsSnapshot{WebhookDead: 4, WebhookPending: 2, ScanConsecutiveFailures: 3, ScanFailingLibraries: 1, DevModeActive: true, DevModeActiveSeconds: 90,
		ConsistencyLastFinished: time.Unix(1700000000, 500000000)}
	for i := range s.ConsistencyFindings {
		s.ConsistencyFindings[i] = int64(i)
	}
	return s
}

// newProductionTestExporter registers every family the production endpoint
// has with images, a store, caches and file logging enabled.
func newProductionTestExporter(t *testing.T, ops app.OpsMetricsSource, blocked BlockedCounter) *Metrics {
	t.Helper()
	source := &imageTestSource{}
	source.set(1)
	m := newImageTestExporter(t, source)
	if err := m.RegisterCaches(cache.Default()); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterOps(ops); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterMemoryLimit(); err != nil {
		t.Fatal(err)
	}
	volumes := []StorageVolume{{Name: "tempdir", Path: "/tmp"}, {Name: "images.storeRoot", Path: "/store"}, {Name: "logging.file", Path: "/logs"}}
	if err := m.RegisterStorage(volumes, func(path string) (StorageUsage, error) {
		if path == "/logs" {
			return StorageUsage{}, errors.New("offline")
		}
		return StorageUsage{TotalBytes: 1000, AvailableBytes: 250}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterClientControl(blocked); err != nil {
		t.Fatal(err)
	}
	return m
}

func scrapeOps(t *testing.T, m *Metrics) map[string]*dto.MetricFamily {
	t.Helper()
	w := requestJobMetrics(m, context.Background())
	if w.Code != http.StatusOK {
		t.Fatalf("scrape status %d: %s", w.Code, w.Body.String())
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(w.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	return families
}

func opsValue(t *testing.T, families map[string]*dto.MetricFamily, name string, labels map[string]string) float64 {
	t.Helper()
	family := families[name]
	if family == nil {
		t.Fatalf("family %s missing", name)
	}
	for _, point := range family.Metric {
		match := len(point.Label) == len(labels)
		for _, l := range point.Label {
			if labels[l.GetName()] != l.GetValue() {
				match = false
			}
		}
		if !match {
			continue
		}
		switch family.GetType() {
		case dto.MetricType_COUNTER:
			return point.GetCounter().GetValue()
		default:
			return point.GetGauge().GetValue()
		}
	}
	t.Fatalf("point %s%v missing", name, labels)
	return 0
}

func TestOpsMetricsExposeTheSharedSnapshot(t *testing.T) {
	previous := debug.SetMemoryLimit(512 << 20)
	t.Cleanup(func() { debug.SetMemoryLimit(previous) })
	ops := &opsTestSource{snapshot: opsTestSnapshot()}
	blocked := &blockedTestSource{}
	blocked.n.Store(42)
	m := newProductionTestExporter(t, ops, blocked)
	families := scrapeOps(t, m)
	for name, want := range map[string]float64{
		"jelee_webhooks_deliveries_dead": 4, "jelee_webhooks_deliveries_pending": 2, "jelee_scan_consecutive_failures": 3, "jelee_scan_failing_libraries": 1,
		"jelee_devmode_active": 1, "jelee_devmode_active_duration_seconds": 90, "jelee_consistency_last_completed_timestamp_seconds": 1700000000.5,
		"jelee_client_control_blocked_total": 42, "jelee_runtime_memory_limit_bytes": 512 << 20,
	} {
		if got := opsValue(t, families, name, nil); got != want {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
	for i, check := range domain.ConsistencyChecks() {
		if got := opsValue(t, families, "jelee_consistency_findings", map[string]string{"check": check}); got != float64(i) {
			t.Fatalf("findings of %s = %v", check, got)
		}
	}
	if got := opsValue(t, families, "jelee_storage_available_bytes", map[string]string{"volume": "images.storeRoot"}); got != 250 {
		t.Fatalf("storage available %v", got)
	}
	if got := opsValue(t, families, "jelee_storage_size_bytes", map[string]string{"volume": "tempdir"}); got != 1000 {
		t.Fatalf("storage size %v", got)
	}
	if len(families["jelee_storage_size_bytes"].Metric) != 2 {
		t.Fatal("an unreadable volume was exported")
	}
	// Every scrape reads the shared snapshot once.
	ops.snapshot.WebhookDead = 9
	if got := opsValue(t, scrapeOps(t, m), "jelee_webhooks_deliveries_dead", nil); got != 9 || ops.calls.Load() != 2 {
		t.Fatalf("second scrape: %v after %d reads", got, ops.calls.Load())
	}
	debug.SetMemoryLimit(math.MaxInt64)
	if got := opsValue(t, scrapeOps(t, m), "jelee_runtime_memory_limit_bytes", nil); got != 0 {
		t.Fatalf("unlimited memory reported as %v", got)
	}
}

func TestOpsMetricsFailClosed(t *testing.T) {
	for name, source := range map[string]*opsTestSource{
		"error":    {err: domain.ErrDatabase},
		"negative": {snapshot: app.OpsMetricsSnapshot{WebhookDead: -1}},
		"nan":      {snapshot: app.OpsMetricsSnapshot{DevModeActive: true, DevModeActiveSeconds: math.NaN()}},
		"inactive": {snapshot: app.OpsMetricsSnapshot{DevModeActiveSeconds: 5}},
		"findings": {snapshot: app.OpsMetricsSnapshot{ConsistencyFindings: domain.ConsistencyCheckCounts{-1}}},
		"timeout":  {wait: true},
	} {
		t.Run(name, func(t *testing.T) {
			m := newProductionTestExporter(t, source, &blockedTestSource{})
			if w := requestJobMetrics(m, context.Background()); w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "jelee_") {
				t.Fatalf("status %d exposed %q", w.Code, w.Body.String())
			}
		})
	}
}

func TestOpsMetricsRegistrationRules(t *testing.T) {
	m := newProductionTestExporter(t, &opsTestSource{}, &blockedTestSource{})
	if err := m.RegisterOps(&opsTestSource{}); err == nil {
		t.Fatal("a second operational source was registered")
	}
	if err := m.RegisterOps(nil); err == nil {
		t.Fatal("nil operational source registered")
	}
	if err := m.RegisterClientControl(nil); err == nil {
		t.Fatal("nil client control registered")
	}
	if err := m.RegisterStorage(nil, nil); err == nil {
		t.Fatal("nil storage reader registered")
	}
	read := func(string) (StorageUsage, error) { return StorageUsage{}, nil }
	if err := m.RegisterStorage([]StorageVolume{{Name: "a", Path: "/a"}, {Name: "a", Path: "/b"}}, read); err == nil {
		t.Fatal("duplicate volume names registered")
	}
	if err := m.RegisterStorage([]StorageVolume{{Name: "", Path: "/a"}}, read); err == nil {
		t.Fatal("unnamed volume registered")
	}
}

func TestStorageSamplerRefreshesOutsideCollection(t *testing.T) {
	now := time.Unix(1700000000, 0)
	var reads atomic.Int64
	release := make(chan struct{})
	s := &storageSampler{volumes: []StorageVolume{{Name: "v", Path: "/v"}}, now: func() time.Time { return now }, stat: func(string) (StorageUsage, error) {
		if reads.Add(1) > 1 {
			<-release
		}
		return StorageUsage{TotalBytes: 10, AvailableBytes: uint64(reads.Load())}, nil
	}}
	s.refresh()
	if got := s.read(); len(got) != 1 || !got[0].ok || got[0].usage.AvailableBytes != 1 {
		t.Fatalf("first reading %+v", got)
	}
	// Old readings start one background refresh; a hung filesystem never
	// blocks a read and never starts a second refresh.
	now = now.Add(storageRefresh)
	for range 3 {
		if got := s.read(); got[0].usage.AvailableBytes != 1 {
			t.Fatal("read waited for the refresh")
		}
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh never finished")
		}
		time.Sleep(time.Millisecond)
	}
	if reads.Load() != 2 {
		t.Fatalf("%d filesystem reads", reads.Load())
	}
	if got := s.read(); got[0].usage.AvailableBytes != 2 {
		t.Fatal("refreshed reading not used")
	}
	s.stat = func(string) (StorageUsage, error) { return StorageUsage{TotalBytes: 1, AvailableBytes: 2}, nil }
	s.refresh()
	if got := s.read(); got[0].ok {
		t.Fatal("an impossible reading was accepted")
	}
}
