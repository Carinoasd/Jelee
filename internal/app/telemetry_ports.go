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
