package postgres

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/jackc/pgx/v5"
)

// QueryLog is the pgx tracer of two SQL logs. The developer mode SQL log
// (G45.5, debug_sql_logging) logs every statement with its duration and
// outcome while its switch returns true; the slow query log (G46.3) logs a
// statement that took at least the configured threshold.
//
// Neither logs arguments. The developer log writes the statement text cut to
// queryLogText bytes, marked with logging.DeveloperSQL: the log router
// writes it, with secret text masked, only while its developer mode switch
// is on. The slow query log writes the statement template
// (logging.SQLTemplate: literals replaced by "?", placeholders kept), which
// the router always accepts.
type QueryLog struct {
	logger  *slog.Logger
	enabled atomic.Pointer[func() bool]
	// slow is the slow query threshold in nanoseconds; zero is off.
	slow atomic.Int64
}

const queryLogText = 512

type queryLogKey struct{}

type queryLogStart struct {
	at  time.Time
	sql string
	dev bool
}

// NewQueryLog returns a switched-off log; SetEnabled connects the developer
// switch and SetSlowThreshold the slow query log.
func NewQueryLog(logger *slog.Logger) *QueryLog { return &QueryLog{logger: logger} }

// SetEnabled installs the switch, normally the developer mode toggle.
func (q *QueryLog) SetEnabled(enabled func() bool) { q.enabled.Store(&enabled) }

// SetSlowThreshold sets the slow query threshold; zero or less is off.
func (q *QueryLog) SetSlowThreshold(threshold time.Duration) { q.slow.Store(int64(max(0, threshold))) }

func (q *QueryLog) on() bool {
	f := q.enabled.Load()
	return f != nil && (*f)()
}

func (q *QueryLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	dev := q.on()
	if !dev && q.slow.Load() <= 0 {
		return ctx
	}
	return context.WithValue(ctx, queryLogKey{}, queryLogStart{at: time.Now(), sql: data.SQL, dev: dev})
}

func (q *QueryLog) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryLogKey{}).(queryLogStart)
	if !ok {
		return
	}
	elapsed := time.Since(start.at)
	if slow := time.Duration(q.slow.Load()); slow > 0 && elapsed >= slow {
		outcome := "ok"
		if data.Err != nil {
			outcome = "failed"
		}
		q.logger.WarnContext(ctx, "slow database query", "component", "db", "code", "db_slow_query", "sql", logging.SQLTemplate(start.sql),
			"durationMs", elapsed.Milliseconds(), "thresholdMs", slow.Milliseconds(), "count", data.CommandTag.RowsAffected(), "outcome", outcome)
	}
	if !start.dev {
		return
	}
	text := strings.Join(strings.Fields(start.sql), " ")
	if len(text) > queryLogText {
		text = text[:queryLogText] + "…"
	}
	q.logger.InfoContext(ctx, "developer mode SQL log", "component", "db", "code", "devmode_sql_log", "statement", logging.DeveloperSQL(text),
		"durationMicros", elapsed.Microseconds(), "rows", data.CommandTag.RowsAffected(), "failed", data.Err != nil)
}

// OpenWithQueryLog is Open with the developer mode SQL log attached.
func OpenWithQueryLog(ctx context.Context, dsn string, maxConnections int32, log *QueryLog) (*Store, error) {
	return open(ctx, dsn, maxConnections, log)
}
