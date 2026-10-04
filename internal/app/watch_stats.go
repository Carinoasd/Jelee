package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// WatchStatsRepository keeps the watch statistics roll-up (G23.3, G23.5)
// and reads it. Reports and exports read only the daily rows, never the
// samples, and apply the viewer's library grants in the same statement
// (G48.3).
type WatchStatsRepository interface {
	// AggregateWatchStats runs one aggregation batch of at most limit
	// pending sessions in one transaction, computing the outcome with
	// compute, and returns how many sessions it aggregated (0 when nothing
	// is pending or another instance is aggregating). ErrConflict means a
	// session changed while it was read; the next run retries.
	AggregateWatchStats(ctx context.Context, limit int, compute func(domain.WatchStatsBatch) (domain.WatchStatsOutcome, error)) (int, error)
	// PurgeWatchStats deletes at most limit daily rows of days before
	// before, returning how many it deleted.
	PurgeWatchStats(ctx context.Context, before time.Time, limit int) (int, error)
	// PendingWatchStats counts ended sessions not aggregated yet.
	PendingWatchStats(ctx context.Context) (int64, error)
	// WatchStatsReport reads a report. The subject must be the actor unless
	// the actor is an administrator; every user (no subject) needs an
	// administrator.
	WatchStatsReport(ctx context.Context, actor domain.Actor, query domain.WatchStatsQuery) (domain.WatchStatsReport, error)
	// AuditWatchStatsExport authorizes an administrator export, refuses one
	// over its limit and records the audit event; it returns the row count.
	AuditWatchStatsExport(ctx context.Context, actor domain.Actor, query domain.WatchStatsExportQuery) (int, error)
	// StreamWatchStatsExport writes the rows of an audited export.
	StreamWatchStatsExport(ctx context.Context, actor domain.Actor, query domain.WatchStatsExportQuery, write func(domain.WatchStatsExportRow) error) error
}

// WatchStatsOptions configures the roll-up and its reports.
type WatchStatsOptions struct {
	Clock Clock
	// Location is the reporting time zone: days are cut at its midnight
	// when sessions are aggregated.
	Location *time.Location
	// SundayWeeks starts weeks on Sunday; weeks start on Monday (ISO 8601)
	// otherwise.
	SundayWeeks bool
	// Interval is the aggregation period; session ends wake it earlier
	// (after WakeDelay, so a burst of stops is aggregated together).
	Interval  time.Duration
	WakeDelay time.Duration
	// Batch bounds the sessions of one aggregation transaction and
	// MaxBatches the transactions of one run.
	Batch      int
	MaxBatches int
	// ExportMaxRows bounds an export.
	ExportMaxRows int
	// Retention deletes daily rows of days older than this; zero keeps
	// them until users clear their history.
	Retention time.Duration
	Rules     domain.WatchStatsRules
	Logger    *slog.Logger
}

// WatchStats aggregates ended playback sessions into the daily roll-up and
// answers statistics requests from it.
type WatchStats struct {
	repo      WatchStatsRepository
	opts      WatchStatsOptions
	weekStart time.Weekday
	wake      chan struct{}

	aggregated atomic.Int64
	runs       atomic.Int64
	conflicts  atomic.Int64

	purgeMu   sync.Mutex
	lastPurge time.Time
}

// WatchStatsCounters are cumulative counters for diagnostics.
type WatchStatsCounters struct {
	Aggregated int64
	Runs       int64
	Conflicts  int64
}

func NewWatchStats(repo WatchStatsRepository, opts WatchStatsOptions) (*WatchStats, error) {
	if repo == nil {
		return nil, domain.ErrInvalid
	}
	if opts.Clock == nil {
		opts.Clock = systemClock{}
	}
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	if opts.Interval == 0 {
		opts.Interval = time.Minute
	}
	if opts.WakeDelay == 0 {
		opts.WakeDelay = 5 * time.Second
	}
	if opts.Batch == 0 {
		opts.Batch = 200
	}
	if opts.MaxBatches == 0 {
		opts.MaxBatches = 50
	}
	if opts.ExportMaxRows == 0 {
		opts.ExportMaxRows = 100_000
	}
	if opts.Rules == (domain.WatchStatsRules{}) {
		opts.Rules = domain.DefaultWatchStatsRules()
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Interval < 0 || opts.WakeDelay < 0 || opts.Batch < 1 || opts.Batch > 1000 ||
		opts.MaxBatches < 1 || opts.ExportMaxRows < 1 || opts.Retention < 0 || opts.Rules.Validate() != nil {
		return nil, domain.ErrInvalid
	}
	w := &WatchStats{repo: repo, opts: opts, weekStart: time.Monday, wake: make(chan struct{}, 1)}
	if opts.SundayWeeks {
		w.weekStart = time.Sunday
	}
	return w, nil
}

// Counters returns the cumulative counters.
func (w *WatchStats) Counters() WatchStatsCounters {
	return WatchStatsCounters{Aggregated: w.aggregated.Load(), Runs: w.runs.Load(), Conflicts: w.conflicts.Load()}
}

// Wake asks the running aggregator to run soon; playback calls it when
// sessions end. It never blocks.
func (w *WatchStats) Wake() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Aggregate runs batches until nothing is pending, another instance holds
// the work, or MaxBatches ran. It returns the sessions aggregated.
func (w *WatchStats) Aggregate(ctx context.Context) (int, error) {
	if w == nil || ctx == nil {
		return 0, domain.ErrInvalid
	}
	w.runs.Add(1)
	compute := func(batch domain.WatchStatsBatch) (domain.WatchStatsOutcome, error) {
		return domain.AggregateWatchStats(batch, w.opts.Rules, w.opts.Location)
	}
	total := 0
	for range w.opts.MaxBatches {
		n, err := w.repo.AggregateWatchStats(ctx, w.opts.Batch, compute)
		if errors.Is(err, domain.ErrConflict) {
			// A session changed under the batch; the next run reads it
			// again.
			w.conflicts.Add(1)
			return total, nil
		}
		if err != nil {
			return total, err
		}
		total += n
		w.aggregated.Add(int64(n))
		if n < w.opts.Batch {
			break
		}
	}
	return total, nil
}

// Run aggregates every Interval, and WakeDelay after a Wake, until ctx ends.
func (w *WatchStats) Run(ctx context.Context) {
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	var delay <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			if delay == nil {
				delay = time.After(w.opts.WakeDelay)
			}
			continue
		case <-delay:
			delay = nil
		case <-ticker.C:
		}
		if _, err := w.Aggregate(ctx); err != nil && ctx.Err() == nil {
			w.opts.Logger.Warn("watch statistics aggregation failed; retrying next interval", "component", "watch_stats")
		}
		if err := w.Purge(ctx); err != nil && ctx.Err() == nil {
			w.opts.Logger.Warn("watch statistics retention purge failed", "component", "watch_stats")
		}
	}
}

// Purge deletes daily rows past the retention period, at most once an hour.
// Days are compared in the reporting time zone.
func (w *WatchStats) Purge(ctx context.Context) error {
	if w == nil || ctx == nil {
		return domain.ErrInvalid
	}
	if w.opts.Retention <= 0 {
		return nil
	}
	w.purgeMu.Lock()
	defer w.purgeMu.Unlock()
	now := w.opts.Clock.Now()
	if !w.lastPurge.IsZero() && now.Sub(w.lastPurge) < purgeEvery {
		return nil
	}
	local := now.In(w.opts.Location).Add(-w.opts.Retention)
	before := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	for {
		n, err := w.repo.PurgeWatchStats(ctx, before, 10*purgeBatch)
		if err != nil {
			return err
		}
		if n < 10*purgeBatch {
			break
		}
	}
	w.lastPurge = now
	return nil
}

// TimeZone is the reporting time zone name.
func (w *WatchStats) TimeZone() string { return w.opts.Location.String() }

// WeekStartName is "monday" or "sunday".
func (w *WatchStats) WeekStartName() string { return strings.ToLower(w.weekStart.String()) }

// ExportMaxRows is the configured export bound.
func (w *WatchStats) ExportMaxRows() int { return w.opts.ExportMaxRows }

func (w *WatchStats) today() time.Time {
	return w.opts.Clock.Now().In(w.opts.Location)
}

// Report answers a statistics request: the actor's own statistics, or as
// an administrator another user's or every user's.
func (w *WatchStats) Report(ctx context.Context, actor domain.Actor, request domain.WatchStatsRequest) (domain.WatchStatsReport, error) {
	if w == nil || ctx == nil {
		return domain.WatchStatsReport{}, domain.ErrInvalid
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.WatchStatsReport{}, domain.ErrUnauthenticated
	}
	q, err := domain.ResolveWatchStatsRequest(request, w.today(), w.weekStart)
	if err != nil {
		return domain.WatchStatsReport{}, err
	}
	report, err := w.repo.WatchStatsReport(ctx, actor, q)
	if err != nil {
		return domain.WatchStatsReport{}, err
	}
	report.TimeZone, report.WeekStart = w.TimeZone(), w.WeekStartName()
	return report, nil
}

// WatchStatsExportRequest is an export request as a client sends it.
type WatchStatsExportRequest struct {
	SubjectID string
	From, To  string
	Format    string
	// Limit lowers the configured bound; 0 keeps it.
	Limit int
}

// Export audits and streams an administrator export: begin is called with
// the row count once the export is authorized, within its limit and
// audited, then write with every row. Nothing is written for a refused
// export.
func (w *WatchStats) Export(ctx context.Context, actor domain.Actor, request WatchStatsExportRequest, begin func(rows int) error, write func(domain.WatchStatsExportRow) error) error {
	if w == nil || ctx == nil || begin == nil || write == nil {
		return domain.ErrInvalid
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.ErrUnauthenticated
	}
	q, err := domain.ResolveWatchStatsRequest(domain.WatchStatsRequest{SubjectID: request.SubjectID, From: request.From, To: request.To, Period: string(domain.WatchPeriodMonth)}, w.today(), w.weekStart)
	if err != nil {
		return err
	}
	export := domain.WatchStatsExportQuery{SubjectID: q.SubjectID, From: q.From, To: q.To, Format: request.Format, Limit: w.opts.ExportMaxRows}
	if export.Format == "" {
		export.Format = domain.WatchStatsExportCSV
	}
	if request.Limit < 0 || request.Limit > w.opts.ExportMaxRows || export.Format != domain.WatchStatsExportCSV && export.Format != domain.WatchStatsExportNDJSON {
		return domain.ErrInvalid
	}
	if request.Limit > 0 {
		export.Limit = request.Limit
	}
	rows, err := w.repo.AuditWatchStatsExport(ctx, actor, export)
	if err != nil {
		return err
	}
	if err = begin(rows); err != nil {
		return err
	}
	return w.repo.StreamWatchStatsExport(ctx, actor, export, write)
}

// WithWatchStats enables watch statistics on a catalog.
func (c *Catalog) WithWatchStats(stats *WatchStats) (*Catalog, error) {
	if c == nil || stats == nil {
		return nil, domain.ErrInvalid
	}
	next := *c
	next.stats = stats
	return &next, nil
}

func (c *Catalog) statsReady(ctx context.Context) error {
	if c == nil || ctx == nil {
		return domain.ErrInvalid
	}
	if c.stats == nil {
		return domain.ErrDatabase
	}
	return ctx.Err()
}

// WatchStats answers a statistics request.
func (c *Catalog) WatchStats(ctx context.Context, actor domain.Actor, request domain.WatchStatsRequest) (domain.WatchStatsReport, error) {
	if err := c.statsReady(ctx); err != nil {
		return domain.WatchStatsReport{}, err
	}
	return c.stats.Report(ctx, actor, request)
}

// ExportWatchStats audits and streams an administrator export.
func (c *Catalog) ExportWatchStats(ctx context.Context, actor domain.Actor, request WatchStatsExportRequest, begin func(rows int) error, write func(domain.WatchStatsExportRow) error) error {
	if err := c.statsReady(ctx); err != nil {
		return err
	}
	return c.stats.Export(ctx, actor, request, begin, write)
}
