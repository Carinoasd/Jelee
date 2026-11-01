package domain

import "testing"

func TestBackgroundJobKinds(t *testing.T) {
	kinds := BackgroundJobKinds()
	for i, kind := range kinds {
		if BackgroundJobKindIndex(kind) != i {
			t.Fatalf("%s at %d", kind, i)
		}
		if i > 0 && kinds[i-1] >= kind {
			t.Fatalf("kinds not sorted: %v", kinds)
		}
	}
	for _, other := range []string{"", JobInventoryScan, JobSubtitleCacheCleanup, "image_variants"} {
		if BackgroundJobKindIndex(other) != -1 || BackgroundJobClusterWide(other) {
			t.Fatalf("%q is a background kind", other)
		}
	}
	if !BackgroundJobClusterWide(JobWebhookDelivery) || !BackgroundJobClusterWide(JobWatchStatsAggregation) || !BackgroundJobClusterWide(JobSubtitleOCR) || BackgroundJobClusterWide(JobPlaybackProgressFlush) {
		t.Fatal("scope of the background kinds")
	}
	if got := BackgroundRunOutcomes(); got != [4]string{"succeeded", "failed", "cancelled", "skipped"} {
		t.Fatalf("outcomes %v", got)
	}
}
