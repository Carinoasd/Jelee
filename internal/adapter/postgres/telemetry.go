package postgres

import "github.com/MoYuanCN/Jelee/internal/app"

var _ app.PoolStatsSource = (*Store)(nil)

// PoolStats reads a single local pgxpool snapshot. Acquisitions and their
// durations include successful attempts; empty waits also exclude cancellation.
func (s *Store) PoolStats() app.PoolStatsSnapshot {
	stat := s.Pool.Stat()
	return app.PoolStatsSnapshot{
		AcquiredConns:        stat.AcquiredConns(),
		IdleConns:            stat.IdleConns(),
		ConstructingConns:    stat.ConstructingConns(),
		TotalConns:           stat.TotalConns(),
		MaxConns:             stat.MaxConns(),
		AcquireCount:         stat.AcquireCount(),
		AcquireDuration:      stat.AcquireDuration(),
		CanceledAcquireCount: stat.CanceledAcquireCount(),
		EmptyAcquireCount:    stat.EmptyAcquireCount(),
		EmptyAcquireWaitTime: stat.EmptyAcquireWaitTime(),
	}
}
