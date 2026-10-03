package telemetry

import (
	"context"
	"errors"
	"math"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ImageStatsSource reads aggregate image processor and store counts. Both
// methods must only read local state; they run inside metric collection.
// StoreStats must report the same ok value for the lifetime of the source.
type ImageStatsSource interface {
	Stats() imageadapter.Stats
	StoreStats() (imageadapter.StoreStats, bool)
}

// NewWithImages adds image cache and store metrics to the production
// exporter. A nil source registers no image instruments. Store instruments are
// registered only when the source has a persistent store, so a disabled store
// is absent rather than reported as an empty cold store.
func NewWithImages(pool app.PoolStatsSource, jobs app.JobMetricsSource, budget *resources.Budget, images ImageStatsSource) (*Metrics, error) {
	m, err := NewWithResources(pool, jobs, budget)
	if err != nil || images == nil {
		return m, err
	}
	if err := m.registerImages(images); err != nil {
		return nil, errors.Join(err, m.Shutdown(context.Background()))
	}
	return m, nil
}

type imageSnapshot struct {
	processor imageadapter.Stats
	store     imageadapter.StoreStats
}

type imageMetric struct {
	name, unit, description string
	counter                 bool
	// One value per point; class-labelled metrics return original, variant.
	values     func(imageSnapshot) []int64
	instrument metric.Int64Observable
}

// Counts are uint64 in the adapter. Saturate instead of wrapping negative.
func imageCount(n uint64) int64 {
	if n > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(n)
}

func imageValue(n int64) []int64 { return []int64{n} }

func imageCounter(read func(imageSnapshot) uint64) func(imageSnapshot) []int64 {
	return func(s imageSnapshot) []int64 { return []int64{imageCount(read(s))} }
}

func imageProcessorMetrics() []imageMetric {
	return []imageMetric{
		{name: "jelee.images.active", description: "Image renders holding a processing slot.", values: func(s imageSnapshot) []int64 { return imageValue(s.processor.Active) }},
		{name: "jelee.images.reserved", unit: "By", description: "Decode memory reserved by active image renders.", values: func(s imageSnapshot) []int64 { return imageValue(s.processor.ReservedBytes) }},
		{name: "jelee.images.requests.admitted", description: "Image renders admitted since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.Admitted })},
		{name: "jelee.images.requests.completed", description: "Image renders that prepared a response since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.Completed })},
		{name: "jelee.images.requests.failed", description: "Admitted image renders that failed since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.Failed })},
		{name: "jelee.images.requests.rejected", description: "Image renders rejected because every processing slot was busy.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.Busy })},
		{name: "jelee.images.decodes", description: "Source images decoded since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.Decodes })},
		{name: "jelee.images.cache.hits", description: "Rendered image memory cache hits since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.CacheHits })},
		{name: "jelee.images.cache.misses", description: "Rendered image memory cache misses since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.CacheMisses })},
		{name: "jelee.images.cache.evictions", description: "Rendered images evicted from the memory cache since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.CacheEvictions })},
		{name: "jelee.images.cache.entries", description: "Rendered images held in the memory cache.", values: func(s imageSnapshot) []int64 { return imageValue(int64(s.processor.CacheEntries)) }},
		{name: "jelee.images.cache.usage", unit: "By", description: "Encoded bytes held in the rendered image memory cache.", values: func(s imageSnapshot) []int64 { return imageValue(s.processor.CacheBytes) }},
	}
}

func imageStoreMetrics() []imageMetric {
	return []imageMetric{
		{name: "jelee.images.store.hits", description: "Persistent image store lookup hits by class since process start.", counter: true, values: func(s imageSnapshot) []int64 {
			return []int64{imageCount(s.store.OriginalHits), imageCount(s.store.VariantHits)}
		}},
		{name: "jelee.images.store.misses", description: "Persistent image store lookup misses by class since process start.", counter: true, values: func(s imageSnapshot) []int64 {
			return []int64{imageCount(s.store.OriginalMisses), imageCount(s.store.VariantMisses)}
		}},
		{name: "jelee.images.store.entries", description: "Objects held in the persistent image store by class.", values: func(s imageSnapshot) []int64 {
			return []int64{int64(s.store.OriginalEntries), int64(s.store.VariantEntries)}
		}},
		{name: "jelee.images.store.usage", unit: "By", description: "Bytes held in the persistent image store by class.", values: func(s imageSnapshot) []int64 {
			return []int64{s.store.OriginalBytes, s.store.VariantBytes}
		}},
		{name: "jelee.images.store.evictions", description: "Objects evicted from the persistent image store since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.store.Evictions })},
		{name: "jelee.images.store.corrupt", description: "Corrupt persistent image store objects detected since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.store.Corrupt })},
		{name: "jelee.images.store.deduplicated", description: "Persistent image store writes satisfied by existing content since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.store.Deduplicated })},
		{name: "jelee.images.store.cleared", description: "Variant clear operations on the persistent image store since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.store.Cleared })},
		{name: "jelee.images.store.remove_errors", description: "Persistent image store removals that failed since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.store.RemoveErrors })},
		{name: "jelee.images.store.foreign", description: "Unrecognized files found in the persistent image store since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.store.Foreign })},
		{name: "jelee.images.store.served", description: "Image responses served from a validated stored variant since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.VariantHits })},
		{name: "jelee.images.store.failures", description: "Failed or rejected store reads and writes since process start; the request still succeeded.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.StoreFailures })},
		{name: "jelee.images.index.failures", description: "Failed image variant index writes since process start; the request still succeeded.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.IndexFailures })},
		{name: "jelee.images.index.evictions", description: "Stored image variants removed through the index since process start.", counter: true, values: imageCounter(func(s imageSnapshot) uint64 { return s.processor.IndexEvictions })},
	}
}

func (m *Metrics) registerImages(source ImageStatsSource) error {
	defs := imageProcessorMetrics()
	_, withStore := source.StoreStats()
	if withStore {
		defs = append(defs, imageStoreMetrics()...)
	}
	// The class label has exactly two fixed values; no digest, path or item
	// identifier ever becomes an attribute.
	classes := []metric.ObserveOption{
		metric.WithAttributeSet(attribute.NewSet(attribute.String("class", "original"))),
		metric.WithAttributeSet(attribute.NewSet(attribute.String("class", "variant"))),
	}
	meter := m.provider.Meter("github.com/MoYuanCN/Jelee/internal/platform/telemetry")
	instruments := make([]metric.Observable, len(defs))
	for i := range defs {
		d := &defs[i]
		// An empty unit is the SDK default and adds no Prometheus suffix.
		var err error
		if d.counter {
			d.instrument, err = meter.Int64ObservableCounter(d.name, metric.WithUnit(d.unit), metric.WithDescription(d.description))
		} else {
			d.instrument, err = meter.Int64ObservableGauge(d.name, metric.WithUnit(d.unit), metric.WithDescription(d.description))
		}
		if err != nil {
			return err
		}
		instruments[i] = d.instrument
	}
	_, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.stopped.Load() {
			return nil
		}
		// One snapshot of each source per collection.
		var s imageSnapshot
		s.processor = source.Stats()
		if withStore {
			s.store, _ = source.StoreStats()
		}
		for _, d := range defs {
			values := d.values(s)
			if len(values) == 1 {
				observer.ObserveInt64(d.instrument, values[0])
				continue
			}
			for j, value := range values {
				observer.ObserveInt64(d.instrument, value, classes[j])
			}
		}
		return nil
	}, instruments...)
	return err
}
