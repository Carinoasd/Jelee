package telemetry

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/platform/cache"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// CacheStatsSource reads local cache counters. Snapshot runs during
// collection and must not perform I/O. *cache.Registry satisfies it.
type CacheStatsSource interface {
	Snapshot() []cache.NamedStats
}

// RegisterCaches exports per-cache counters, occupancy, and lifetime hit
// ratio. The only label is the cache name, which registries restrict to code
// constants, so cardinality stays bounded. Call it at most once per Metrics.
func (m *Metrics) RegisterCaches(source CacheStatsSource) error {
	if source == nil {
		return errors.New("telemetry requires a cache stats source")
	}
	meter := m.provider.Meter("github.com/MoYuanCN/Jelee/internal/platform/telemetry")
	type counter struct {
		name, description string
		value             func(cache.Stats) uint64
		instrument        metric.Int64ObservableCounter
	}
	counters := []counter{
		{name: "jelee.cache.hits", description: "Cache lookups that returned a live entry.", value: func(s cache.Stats) uint64 { return s.Hits }},
		{name: "jelee.cache.misses", description: "Cache lookups that found no live entry.", value: func(s cache.Stats) uint64 { return s.Misses }},
		{name: "jelee.cache.evictions", description: "Entries removed for capacity or memory pressure.", value: func(s cache.Stats) uint64 { return s.Evictions }},
		{name: "jelee.cache.expirations", description: "Entries removed after their TTL.", value: func(s cache.Stats) uint64 { return s.Expirations }},
	}
	instruments := make([]metric.Observable, 0, len(counters)+3)
	for i := range counters {
		instrument, err := meter.Int64ObservableCounter(counters[i].name, metric.WithDescription(counters[i].description))
		if err != nil {
			return err
		}
		counters[i].instrument = instrument
		instruments = append(instruments, instrument)
	}
	entries, err := meter.Int64ObservableGauge("jelee.cache.entries", metric.WithDescription("Current cache entries."))
	if err != nil {
		return err
	}
	bytes, err := meter.Int64ObservableGauge("jelee.cache.size", metric.WithUnit("By"), metric.WithDescription("Current estimated cache bytes."))
	if err != nil {
		return err
	}
	ratio, err := meter.Float64ObservableGauge("jelee.cache.hit.ratio", metric.WithDescription("Lifetime cache hit ratio; zero before the first lookup."))
	if err != nil {
		return err
	}
	instruments = append(instruments, entries, bytes, ratio)
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.stopped.Load() {
			return nil
		}
		for _, named := range source.Snapshot() {
			labels := metric.WithAttributeSet(attribute.NewSet(attribute.String("cache", named.Name)))
			for _, c := range counters {
				observer.ObserveInt64(c.instrument, clampInt64(c.value(named.Stats)), labels)
			}
			observer.ObserveInt64(entries, int64(named.Stats.Entries), labels)
			observer.ObserveInt64(bytes, named.Stats.Bytes, labels)
			observer.ObserveFloat64(ratio, named.Stats.HitRatio(), labels)
		}
		return nil
	}, instruments...)
	return err
}

func clampInt64(v uint64) int64 {
	if v > 1<<63-1 {
		return 1<<63 - 1
	}
	return int64(v)
}
