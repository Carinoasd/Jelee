package app

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Audit retention purge (G46.9). Audit rows are append-only; the database
// lets purge_audit_logs() delete a row only once it is older than the
// retention of its category (audit_retention). AuditJanitor calls that path
// on a fixed interval: each pass removes at most MaxBatches batches of Batch
// rows, so a backlog drains over several passes without one long
// transaction, and every pass that removed rows is logged and audited
// (audit.retention_purged, written by the repository).

// AuditPurger is the repository side of the purge.
type AuditPurger interface {
	PurgeAudit(ctx context.Context, batch int) (domain.AuditPurgeResult, error)
}

// AuditArchivePurger is the repository side of an archiving purge (G36.3): it
// hands the expired rows to the sink and deletes them only after the sink
// returned, in one transaction.
type AuditArchivePurger interface {
	PurgeAuditArchived(ctx context.Context, batch int, sink domain.AuditArchiveSink) (domain.AuditPurgeResult, error)
}

// AuditArchiver is the archive the janitor writes to before it deletes, and
// whose own retention it enforces after each pass. Pruning never touches the
// database.
type AuditArchiver interface {
	domain.AuditArchiveSink
	Prune(ctx context.Context, now time.Time) (domain.AuditArchivePrune, error)
}

// Bounds of the janitor settings; the batch bound is the database's own.
const (
	AuditPurgeBatchMax      = 10000
	AuditPurgeMaxBatchesMax = 1000
)

// AuditJanitorOptions configures an AuditJanitor.
type AuditJanitorOptions struct {
	// Interval separates passes; the first pass runs one interval after Run
	// starts, so a restart loop never purges in a burst.
	Interval time.Duration
	// Batch bounds the rows one transaction deletes (1..AuditPurgeBatchMax).
	Batch int
	// MaxBatches bounds the batches of one pass (1..AuditPurgeMaxBatchesMax).
	MaxBatches int
	// Timeout bounds one batch; zero means one minute.
	Timeout time.Duration
	Logger  *slog.Logger
	// Archive, when set, receives every batch before its rows are deleted;
	// the repository must then implement AuditArchivePurger. Nil keeps the
	// plain delete-only purge.
	Archive AuditArchiver
}

// AuditJanitor runs the scheduled audit retention purge.
type AuditJanitor struct {
	repo    AuditPurger
	archive AuditArchivePurger
	opts    AuditJanitorOptions
	// Totals since start, read by the metrics callbacks.
	archivedRows    atomic.Uint64
	archiveFailures atomic.Uint64
	prunedFiles     atomic.Uint64
}

// ArchivedRows is the rows written to the archive since start.
func (j *AuditJanitor) ArchivedRows() uint64 { return j.archivedRows.Load() }

// ArchiveFailures counts archive writes and archive retention passes that
// failed since start; a failed write leaves its rows in the database.
func (j *AuditJanitor) ArchiveFailures() uint64 { return j.archiveFailures.Load() }

// ArchivePrunedFiles is the archive files its own retention removed.
func (j *AuditJanitor) ArchivePrunedFiles() uint64 { return j.prunedFiles.Load() }

// Archiving reports whether purged rows are archived first.
func (j *AuditJanitor) Archiving() bool { return j.archive != nil }

// countingSink counts the janitor's archive outcomes around the real sink.
type countingSink struct {
	j    *AuditJanitor
	sink domain.AuditArchiveSink
}

func (c countingSink) Archive(ctx context.Context, records []domain.AuditRecord) (domain.AuditArchiveReceipt, error) {
	receipt, err := c.sink.Archive(ctx, records)
	if err != nil {
		if ctx.Err() == nil {
			c.j.archiveFailures.Add(1)
		}
		return receipt, err
	}
	c.j.archivedRows.Add(uint64(max(len(records), 0))) //nolint:gosec // a length is never negative
	return receipt, nil
}

// NewAuditJanitor validates opts; every bound is required.
func NewAuditJanitor(repo AuditPurger, opts AuditJanitorOptions) (*AuditJanitor, error) {
	if repo == nil || opts.Logger == nil || opts.Interval <= 0 || opts.Batch < 1 || opts.Batch > AuditPurgeBatchMax ||
		opts.MaxBatches < 1 || opts.MaxBatches > AuditPurgeMaxBatchesMax || opts.Timeout < 0 {
		return nil, domain.ErrInvalid
	}
	if opts.Timeout == 0 {
		opts.Timeout = time.Minute
	}
	j := &AuditJanitor{repo: repo, opts: opts}
	if opts.Archive != nil {
		archive, ok := repo.(AuditArchivePurger)
		if !ok {
			return nil, domain.ErrInvalid
		}
		j.archive = archive
	}
	return j, nil
}

// AuditPassResult is one pass: the rows removed per category, the batches
// run and whether expired rows may remain because the pass hit MaxBatches.
type AuditPassResult struct {
	domain.AuditPurgeResult
	Batches int
	Limited bool
}

// Pass purges until a batch comes back short or MaxBatches ran. A failed
// batch ends the pass; rows removed before it stay removed.
func (j *AuditJanitor) Pass(ctx context.Context) (AuditPassResult, error) {
	var result AuditPassResult
	for result.Batches < j.opts.MaxBatches {
		batchCtx, cancel := context.WithTimeout(ctx, j.opts.Timeout)
		var got domain.AuditPurgeResult
		var err error
		if j.archive != nil {
			got, err = j.archive.PurgeAuditArchived(batchCtx, j.opts.Batch, countingSink{j: j, sink: j.opts.Archive})
		} else {
			got, err = j.repo.PurgeAudit(batchCtx, j.opts.Batch)
		}
		cancel()
		if err != nil {
			return result, err
		}
		result.Batches++
		result.Audit += got.Audit
		result.Security += got.Security
		if got.Audit+got.Security < int64(j.opts.Batch) {
			return result, nil
		}
	}
	result.Limited = true
	return result, nil
}

// Run purges every Interval until ctx ends.
func (j *AuditJanitor) Run(ctx context.Context) {
	ticker := time.NewTicker(j.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		j.runPass(ctx)
		j.pruneArchive(ctx)
	}
}

// pruneArchive applies the archive's own retention and size cap. It runs after
// every pass, also when the purge failed, and only removes archive files.
func (j *AuditJanitor) pruneArchive(ctx context.Context) {
	if j.archive == nil {
		return
	}
	pruned, err := j.opts.Archive.Prune(ctx, time.Now())
	j.prunedFiles.Add(uint64(max(pruned.Files, 0))) //nolint:gosec // clamped above zero
	switch {
	case err != nil && ctx.Err() != nil:
	case err != nil:
		j.archiveFailures.Add(1)
		j.opts.Logger.Warn("audit archive retention failed; retrying next interval", "component", "gc", "files", pruned.Files)
	case pruned.Files > 0:
		j.opts.Logger.Info("audit archive retention removed old archive files", "component", "gc", "files", pruned.Files, "bytes", pruned.Bytes)
	}
}

func (j *AuditJanitor) runPass(ctx context.Context) {
	started := time.Now()
	result, err := j.Pass(ctx)
	removed := result.Audit + result.Security
	attrs := []any{"component", "gc", "count", removed, "auditRows", result.Audit, "securityRows", result.Security,
		"batches", result.Batches, "durationMs", time.Since(started).Milliseconds()}
	switch {
	case err != nil && ctx.Err() != nil:
		// Shutdown interrupted the pass; the next start resumes it.
	case err != nil && j.archive != nil:
		// The archive write failed or the purge disagreed with it: nothing
		// was deleted for that batch, so no audit row is lost.
		j.opts.Logger.Warn("audit retention purge failed; expired rows stay in the database and the next interval retries", append(attrs, "archive", true, "error", err.Error())...)
	case err != nil:
		j.opts.Logger.Warn("audit retention purge failed; retrying next interval", attrs...)
	case result.Limited:
		j.opts.Logger.Info("audit retention purge reached its batch limit; continuing next interval", attrs...)
	case removed > 0:
		j.opts.Logger.Info("audit retention purge removed expired rows", attrs...)
	}
}
