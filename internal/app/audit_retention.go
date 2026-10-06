package app

import (
	"context"
	"log/slog"
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
}

// AuditJanitor runs the scheduled audit retention purge.
type AuditJanitor struct {
	repo AuditPurger
	opts AuditJanitorOptions
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
	return &AuditJanitor{repo: repo, opts: opts}, nil
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
		got, err := j.repo.PurgeAudit(batchCtx, j.opts.Batch)
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
	case err != nil:
		j.opts.Logger.Warn("audit retention purge failed; retrying next interval", attrs...)
	case result.Limited:
		j.opts.Logger.Info("audit retention purge reached its batch limit; continuing next interval", attrs...)
	case removed > 0:
		j.opts.Logger.Info("audit retention purge removed expired rows", attrs...)
	}
}
