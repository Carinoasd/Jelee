package domain

// Background job kinds (G41.1): recurring work the job executor paces on
// every instance instead of a goroutine of its own per subsystem. They are
// not rows of the jobs table: a run every second (webhook polling) or every
// ten seconds (progress flush) would flood the queue and take the anchor
// library's single active slot from its scans. The executor gives them the
// rest of the job model: a fixed whitelist, background priority, the shared
// work budget, cancellation with the service lifetime, one instance at a
// time for cluster-wide work (a database advisory lock) and the job metric
// dimensions.
const (
	// JobWebhookDelivery fans outbox events out and delivers due webhook
	// deliveries (G12.3). Cluster-wide.
	JobWebhookDelivery = "webhook_delivery"
	// JobWatchStatsAggregation rolls ended playback sessions up into the
	// daily statistics and applies their retention (G23.5). Cluster-wide.
	JobWatchStatsAggregation = "watch_stats_aggregation"
	// JobPlaybackProgressFlush writes the playback reports this instance
	// buffered and closes stale sessions (G23.2). Per instance: the buffer
	// lives in this process's memory, so every instance flushes its own.
	JobPlaybackProgressFlush = "playback_progress_flush"
	// JobSubtitleOCR recognizes the next source of this instance's subtitle
	// OCR queue (G15.6). Cluster-wide: OCR is the heaviest CPU work of the
	// service, so one source is recognized across all instances at a time;
	// each run takes one queued source and releases the lock afterwards.
	JobSubtitleOCR = "subtitle_ocr"
)

// BackgroundJobPriority is the priority dimension of every background job.
const BackgroundJobPriority = "background"

// Outcomes of one background job run. Skipped runs never started: another
// instance held the cluster-wide lock or the shared budget had no room.
const (
	BackgroundRunSucceeded = "succeeded"
	BackgroundRunFailed    = "failed"
	BackgroundRunCancelled = "cancelled"
	BackgroundRunSkipped   = "skipped"
)

// BackgroundJobKindCount is the number of background kinds.
const BackgroundJobKindCount = 4

// BackgroundJobKinds lists the background kinds in a stable order; metric
// snapshots keep this order.
func BackgroundJobKinds() [BackgroundJobKindCount]string {
	return [BackgroundJobKindCount]string{JobPlaybackProgressFlush, JobSubtitleOCR, JobWatchStatsAggregation, JobWebhookDelivery}
}

// BackgroundRunOutcomes lists the run outcomes in a stable order.
func BackgroundRunOutcomes() [4]string {
	return [4]string{BackgroundRunSucceeded, BackgroundRunFailed, BackgroundRunCancelled, BackgroundRunSkipped}
}

// BackgroundJobKindIndex is kind's position in BackgroundJobKinds, or -1
// for a kind outside the whitelist.
func BackgroundJobKindIndex(kind string) int {
	for i, k := range BackgroundJobKinds() {
		if k == kind {
			return i
		}
	}
	return -1
}

// BackgroundJobClusterWide reports a kind that must run on one instance at a
// time; the others run on every instance.
func BackgroundJobClusterWide(kind string) bool {
	return kind == JobWebhookDelivery || kind == JobWatchStatsAggregation || kind == JobSubtitleOCR
}
