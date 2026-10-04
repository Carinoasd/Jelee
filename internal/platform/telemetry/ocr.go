package telemetry

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/adapter/subtitleocr"
	"go.opentelemetry.io/otel/metric"
)

// SubtitleOCRStats reports the background subtitle OCR counters (G15.6). It
// must only read memory.
type SubtitleOCRStats interface {
	Stats() subtitleocr.Stats
}

// RegisterSubtitleOCR exports the queue, outcome and picture counters of
// subtitle OCR, including the pictures that waited for the per-minute limit.
func (m *Metrics) RegisterSubtitleOCR(source SubtitleOCRStats) error {
	if source == nil {
		return errors.New("telemetry requires a subtitle OCR source")
	}
	meter := m.provider.Meter(telemetryMeter)
	type counter struct {
		name, description string
		value             func(subtitleocr.Stats) uint64
		instrument        metric.Int64ObservableCounter
	}
	counters := []counter{
		{name: "jelee.subtitle_ocr.jobs.completed", description: "Sources whose bitmap subtitles OCR finished since process start.", value: func(s subtitleocr.Stats) uint64 { return s.Completed }},
		{name: "jelee.subtitle_ocr.jobs.failed", description: "OCR jobs that failed since process start.", value: func(s subtitleocr.Stats) uint64 { return s.Failed }},
		{name: "jelee.subtitle_ocr.jobs.dropped", description: "OCR requests dropped because the queue was full.", value: func(s subtitleocr.Stats) uint64 { return s.Dropped }},
		{name: "jelee.subtitle_ocr.pictures", description: "Bitmap subtitle pictures decoded for OCR.", value: func(s subtitleocr.Stats) uint64 { return s.Pictures }},
		{name: "jelee.subtitle_ocr.recognized", description: "Pictures Tesseract recognized.", value: func(s subtitleocr.Stats) uint64 { return s.Recognized }},
		{name: "jelee.subtitle_ocr.reused", description: "Pictures identical to an earlier one of the same track, recognized once.", value: func(s subtitleocr.Stats) uint64 { return s.Reused }},
		{name: "jelee.subtitle_ocr.rejected", description: "Pictures Tesseract refused or timed out on; their cues are left out.", value: func(s subtitleocr.Stats) uint64 { return s.Rejected }},
		{name: "jelee.subtitle_ocr.skipped", description: "Pictures the decoder skipped as corrupt.", value: func(s subtitleocr.Stats) uint64 { return s.Skipped }},
		{name: "jelee.subtitle_ocr.throttled", description: "Pictures that waited for the per-minute OCR limit.", value: func(s subtitleocr.Stats) uint64 { return s.Throttled }},
	}
	instruments := make([]metric.Observable, 0, len(counters)+2)
	for i := range counters {
		instrument, err := meter.Int64ObservableCounter(counters[i].name, metric.WithDescription(counters[i].description))
		if err != nil {
			return err
		}
		counters[i].instrument = instrument
		instruments = append(instruments, instrument)
	}
	queued, err := meter.Int64ObservableGauge("jelee.subtitle_ocr.queued", metric.WithDescription("Sources waiting for OCR."))
	if err != nil {
		return err
	}
	running, err := meter.Int64ObservableGauge("jelee.subtitle_ocr.running", metric.WithDescription("Sources being processed by OCR (0 or 1)."))
	if err != nil {
		return err
	}
	instruments = append(instruments, queued, running)
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.stopped.Load() {
			return nil
		}
		stats := source.Stats()
		for _, c := range counters {
			observer.ObserveInt64(c.instrument, int64(min(c.value(stats), 1<<62))) //nolint:gosec // G115: clamped below MaxInt64
		}
		observer.ObserveInt64(queued, int64(stats.Queued))
		observer.ObserveInt64(running, int64(stats.Running))
		return nil
	}, instruments...)
	return err
}
