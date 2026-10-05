package telemetry

import (
	"context"
	"errors"
	"math"
	"runtime/debug"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Operational metrics behind the default alert rules (G50.6,
// deploy/prometheus/jelee-alerts.yml). Shared database values are read once
// per admitted scrape, within the same two-second prefetch budget as the job
// metrics; process values are read from memory during collection.

const telemetryMeter = "github.com/MoYuanCN/Jelee/internal/platform/telemetry"

type opsSource struct {
	source app.OpsMetricsSource
}

func validOpsSnapshot(s app.OpsMetricsSnapshot) bool {
	if s.WebhookDead < 0 || s.WebhookPending < 0 || s.ScanConsecutiveFailures < 0 || s.ScanFailingLibraries < 0 ||
		!finiteNonnegative(s.DevModeActiveSeconds) || !s.DevModeActive && s.DevModeActiveSeconds != 0 {
		return false
	}
	for _, n := range s.ConsistencyFindings {
		if n < 0 {
			return false
		}
	}
	return true
}

// prefetch reads one snapshot. Failures, a context error or an invalid
// snapshot return false; the scrape then fails instead of exposing stale or
// partial values.
func (o *opsSource) prefetch(parent context.Context, deadline time.Time) (app.OpsMetricsSnapshot, bool) {
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	snapshot, err := o.source.OpsMetrics(ctx)
	if err != nil || ctx.Err() != nil || !validOpsSnapshot(snapshot) {
		return app.OpsMetricsSnapshot{}, false
	}
	return snapshot, true
}

// RegisterOps exports the shared operational gauges: dead-lettered and
// pending webhook deliveries, consecutive scan failures, the developer mode
// switch and the latest consistency findings per check. Call it once,
// before the handler serves.
func (m *Metrics) RegisterOps(source app.OpsMetricsSource) error {
	if source == nil {
		return errors.New("telemetry requires an operational metrics source")
	}
	meter := m.provider.Meter(telemetryMeter)
	type gauge struct {
		name, unit, description string
		value                   func(app.OpsMetricsSnapshot) float64
		instrument              metric.Float64ObservableGauge
	}
	gauges := []gauge{
		{name: "jelee.webhooks.deliveries.dead", description: "Webhook deliveries that exhausted their attempts and wait for a manual replay.", value: func(s app.OpsMetricsSnapshot) float64 { return float64(s.WebhookDead) }},
		{name: "jelee.webhooks.deliveries.pending", description: "Webhook deliveries waiting for their next attempt.", value: func(s app.OpsMetricsSnapshot) float64 { return float64(s.WebhookPending) }},
		{name: "jelee.scan.consecutive_failures", description: "Longest run of failed inventory scans at the end of any library's retained history.", value: func(s app.OpsMetricsSnapshot) float64 { return float64(s.ScanConsecutiveFailures) }},
		{name: "jelee.scan.failing_libraries", description: "Libraries whose latest finished inventory scan failed.", value: func(s app.OpsMetricsSnapshot) float64 { return float64(s.ScanFailingLibraries) }},
		{name: "jelee.devmode.active", description: "One while an unexpired developer mode session is on, else zero.", value: func(s app.OpsMetricsSnapshot) float64 {
			if s.DevModeActive {
				return 1
			}
			return 0
		}},
		{name: "jelee.devmode.active_duration", unit: "s", description: "Seconds the current developer mode session has been on; zero when off.", value: func(s app.OpsMetricsSnapshot) float64 { return s.DevModeActiveSeconds }},
		{name: "jelee.consistency.last_completed_timestamp", unit: "s", description: "Unix time of the newest finished consistency check run; zero before the first.", value: func(s app.OpsMetricsSnapshot) float64 {
			if s.ConsistencyLastFinished.IsZero() {
				return 0
			}
			return float64(s.ConsistencyLastFinished.UnixNano()) / 1e9
		}},
	}
	instruments := make([]metric.Observable, 0, len(gauges)+1)
	for i := range gauges {
		options := []metric.Float64ObservableGaugeOption{metric.WithDescription(gauges[i].description)}
		if gauges[i].unit != "" {
			options = append(options, metric.WithUnit(gauges[i].unit))
		}
		instrument, err := meter.Float64ObservableGauge(gauges[i].name, options...)
		if err != nil {
			return err
		}
		gauges[i].instrument = instrument
		instruments = append(instruments, instrument)
	}
	findings, err := meter.Int64ObservableGauge("jelee.consistency.findings", metric.WithDescription("Findings of the latest finished consistency check of every library, per check."))
	if err != nil {
		return err
	}
	instruments = append(instruments, findings)
	if !m.ops.CompareAndSwap(nil, &opsSource{source: source}) {
		return errors.New("telemetry operational source already registered")
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.stopped.Load() {
			return nil
		}
		s := m.opsActive.Load()
		if s == nil {
			return nil
		}
		for _, g := range gauges {
			observer.ObserveFloat64(g.instrument, g.value(*s))
		}
		for i, check := range domain.ConsistencyChecks() {
			observer.ObserveInt64(findings, s.ConsistencyFindings[i], metric.WithAttributeSet(attribute.NewSet(attribute.String("check", check))))
		}
		return nil
	}, instruments...)
	return err
}

// BlockedCounter reports requests the client control (G47) refused since
// the process started. It must only read memory.
type BlockedCounter interface {
	BlockedTotal() int64
}

// RegisterClientControl exports the refused request counter. The burst
// alert (G47.8) is a rate over it.
func (m *Metrics) RegisterClientControl(source BlockedCounter) error {
	if source == nil {
		return errors.New("telemetry requires a client control source")
	}
	meter := m.provider.Meter(telemetryMeter)
	counter, err := meter.Int64ObservableCounter("jelee.client_control.blocked", metric.WithDescription("Requests denied or held for approval by enforced client control rules since process start."))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !m.stopped.Load() {
			observer.ObserveInt64(counter, max(0, source.BlockedTotal()))
		}
		return nil
	}, counter)
	return err
}

// memoryLimit reads the Go soft memory limit without changing it. No limit
// (math.MaxInt64) reads as zero so a heap ratio alert stays silent.
func memoryLimit() int64 {
	limit := debug.SetMemoryLimit(-1)
	if limit <= 0 || limit == math.MaxInt64 {
		return 0
	}
	return limit
}

// RegisterMemoryLimit exports the Go soft memory limit (GOMEMLIMIT), the
// reference of the memory alert.
func (m *Metrics) RegisterMemoryLimit() error {
	meter := m.provider.Meter(telemetryMeter)
	gauge, err := meter.Int64ObservableGauge("jelee.runtime.memory_limit", metric.WithUnit("By"), metric.WithDescription("Go soft memory limit in bytes; zero when unlimited."))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !m.stopped.Load() {
			observer.ObserveInt64(gauge, memoryLimit())
		}
		return nil
	}, gauge)
	return err
}

// StorageVolume names a directory whose filesystem is watched. Name is a
// fixed configuration key, never a path.
type StorageVolume struct {
	Name string
	Path string
}

// StorageUsage is one filesystem reading.
type StorageUsage struct {
	TotalBytes     uint64
	AvailableBytes uint64
}

// StorageStat reads the filesystem holding path.
type StorageStat func(path string) (StorageUsage, error)

// storageRefresh bounds filesystem reads: at most one per volume per
// interval, outside collection.
const storageRefresh = 30 * time.Second

type storageReading struct {
	ok    bool
	usage StorageUsage
}

// storageSampler keeps the last readings. A scrape that finds them old
// starts one background refresh and reports what it has, so a hung
// filesystem can delay readings but never a scrape, and at most one refresh
// runs at a time.
type storageSampler struct {
	volumes  []StorageVolume
	stat     StorageStat
	now      func() time.Time
	mu       sync.Mutex
	readings []storageReading
	sampled  time.Time
	running  bool
}

func (s *storageSampler) refresh() {
	readings := make([]storageReading, len(s.volumes))
	for i, v := range s.volumes {
		usage, err := s.stat(v.Path)
		readings[i] = storageReading{ok: err == nil && usage.TotalBytes > 0 && usage.AvailableBytes <= usage.TotalBytes, usage: usage}
	}
	s.mu.Lock()
	s.readings, s.sampled, s.running = readings, s.now(), false
	s.mu.Unlock()
}

func (s *storageSampler) read() []storageReading {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running && s.now().Sub(s.sampled) >= storageRefresh {
		s.running = true
		go s.refresh()
	}
	return append([]storageReading(nil), s.readings...)
}

// RegisterStorage exports available and total bytes of each volume's
// filesystem, labelled by the volume name. The first readings are taken
// before it returns, waiting at most two seconds.
func (m *Metrics) RegisterStorage(volumes []StorageVolume, stat StorageStat) error {
	if stat == nil {
		return errors.New("telemetry requires a storage reader")
	}
	seen := map[string]bool{}
	for _, v := range volumes {
		if v.Name == "" || v.Path == "" || seen[v.Name] {
			return errors.New("telemetry storage volumes need unique names and paths")
		}
		seen[v.Name] = true
	}
	sampler := &storageSampler{volumes: append([]StorageVolume(nil), volumes...), stat: stat, now: time.Now, running: true}
	done := make(chan struct{})
	go func() {
		sampler.refresh()
		close(done)
	}()
	timer := time.NewTimer(2 * time.Second)
	select {
	case <-done:
	case <-timer.C:
	}
	timer.Stop()
	meter := m.provider.Meter(telemetryMeter)
	available, err := meter.Int64ObservableGauge("jelee.storage.available", metric.WithUnit("By"), metric.WithDescription("Bytes available to the service on the filesystem of a configured directory."))
	if err != nil {
		return err
	}
	size, err := meter.Int64ObservableGauge("jelee.storage.size", metric.WithUnit("By"), metric.WithDescription("Total bytes of the filesystem of a configured directory."))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.stopped.Load() {
			return nil
		}
		for i, r := range sampler.read() {
			if !r.ok {
				continue
			}
			labels := metric.WithAttributeSet(attribute.NewSet(attribute.String("volume", sampler.volumes[i].Name)))
			observer.ObserveInt64(available, clampInt64(r.usage.AvailableBytes), labels)
			observer.ObserveInt64(size, clampInt64(r.usage.TotalBytes), labels)
		}
		return nil
	}, available, size)
	return err
}
