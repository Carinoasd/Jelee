package telemetry

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/subtitleocr"
)

type ocrStats struct{ stats subtitleocr.Stats }

func (o ocrStats) Stats() subtitleocr.Stats { return o.stats }

func TestSubtitleOCRMetrics(t *testing.T) {
	source := &imageTestSource{}
	source.set(1)
	m := newImageTestExporter(t, source)
	stats := ocrStats{subtitleocr.Stats{Queued: 2, Running: 1, Completed: 5, Failed: 1, Dropped: 3, Pictures: 900, Recognized: 700, Reused: 150, Rejected: 4, Skipped: 2, Throttled: 40}}
	if err := m.RegisterSubtitleOCR(stats); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterSubtitleOCR(nil); err == nil {
		t.Fatal("nil source accepted")
	}
	families := scrapeOps(t, m)
	for name, want := range map[string]float64{
		"jelee_subtitle_ocr_jobs_completed_total": 5, "jelee_subtitle_ocr_jobs_failed_total": 1, "jelee_subtitle_ocr_jobs_dropped_total": 3,
		"jelee_subtitle_ocr_pictures_total": 900, "jelee_subtitle_ocr_recognized_total": 700, "jelee_subtitle_ocr_reused_total": 150,
		"jelee_subtitle_ocr_rejected_total": 4, "jelee_subtitle_ocr_skipped_total": 2, "jelee_subtitle_ocr_throttled_total": 40,
		"jelee_subtitle_ocr_queued": 2, "jelee_subtitle_ocr_running": 1,
	} {
		if got := opsValue(t, families, name, map[string]string{}); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}
