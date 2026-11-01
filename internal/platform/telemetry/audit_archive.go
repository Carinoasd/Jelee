package telemetry

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/metric"
)

// AuditArchiveCounters reports the audit archive since the process started
// (G36.3). It must only read memory.
type AuditArchiveCounters interface {
	ArchivedRows() uint64
	ArchiveFailures() uint64
	ArchivePrunedFiles() uint64
}

// RegisterAuditArchive exports the archive counters. The default alert
// JeleeAuditArchiveFailed is an increase over the failure counter.
func (m *Metrics) RegisterAuditArchive(source AuditArchiveCounters) error {
	if source == nil {
		return errors.New("telemetry requires an audit archive counter source")
	}
	meter := m.provider.Meter(telemetryMeter)
	rows, err := meter.Int64ObservableCounter("jelee.audit.archive.rows", metric.WithDescription("Audit and security rows written to the archive before their retention purge since process start."))
	if err != nil {
		return err
	}
	failures, err := meter.Int64ObservableCounter("jelee.audit.archive.failures", metric.WithDescription("Archive writes or archive retention passes that failed since process start; a failed write keeps its rows in the database."))
	if err != nil {
		return err
	}
	pruned, err := meter.Int64ObservableCounter("jelee.audit.archive.pruned_files", metric.WithDescription("Archive files removed by the archive's own retention or size cap since process start."))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !m.stopped.Load() {
			observer.ObserveInt64(rows, counterValue(source.ArchivedRows()))
			observer.ObserveInt64(failures, counterValue(source.ArchiveFailures()))
			observer.ObserveInt64(pruned, counterValue(source.ArchivePrunedFiles()))
		}
		return nil
	}, rows, failures, pruned)
	return err
}
