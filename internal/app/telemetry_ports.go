package app

import "time"

// PoolStatsSource reads local pool counters without acquiring a connection or
// executing SQL. Implementations must be safe for concurrent calls.
type PoolStatsSource interface {
	PoolStats() PoolStatsSnapshot
}

// PoolStatsSnapshot contains one pool snapshot. Counts and durations are
// cumulative since the pool was created; connection counts are current values.
type PoolStatsSnapshot struct {
	AcquiredConns        int32
	IdleConns            int32
	ConstructingConns    int32
	TotalConns           int32
	MaxConns             int32
	AcquireCount         int64
	AcquireDuration      time.Duration
	CanceledAcquireCount int64
	EmptyAcquireCount    int64
	EmptyAcquireWaitTime time.Duration
}

// ImageProcessorStats contains aggregate image processor counts only; source
// identifiers and paths never become labels. Completed means a response was
// prepared, not delivered.
type ImageProcessorStats struct {
	Active, ReservedBytes, MaxEstimatedImageBytes int64
	Admitted, Completed, Failed, Busy             uint64
	CacheHits, CacheMisses, Decodes               uint64
	CacheEntries                                  int
	CacheBytes                                    int64
	CacheEvictions                                uint64
	// Persistent store: variant hits, failed store or index writes (requests
	// still succeed) and variants removed through the index.
	VariantHits, StoreFailures, IndexFailures, IndexEvictions uint64
	// Generated counts variants rendered from a decoded source because
	// neither the memory cache nor the store had them (G40.13).
	Generated uint64
}

// ImageStoreStats contains aggregate persistent image store counts only;
// digests and paths never become labels. Hits and misses per class provide
// the G42.4 hit-rate signal.
type ImageStoreStats struct {
	OriginalEntries, VariantEntries           int
	OriginalBytes, VariantBytes               int64
	OriginalHits, OriginalMisses              uint64
	VariantHits, VariantMisses                uint64
	Evictions, Corrupt, Deduplicated, Cleared uint64
	RemoveErrors, Foreign                     uint64
}

// SubtitleOCRStats are aggregate subtitle OCR counters without paths, IDs or
// text.
type SubtitleOCRStats struct {
	Queued     int
	Running    int
	Completed  uint64
	Failed     uint64
	Dropped    uint64
	Pictures   uint64
	Recognized uint64
	Reused     uint64
	Rejected   uint64
	Skipped    uint64
	Throttled  uint64
}
