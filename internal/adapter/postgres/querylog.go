package postgres

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueryLog is the developer mode SQL log (G45.5, debug_sql_logging): while
// its switch returns true every statement is logged with its duration and
// outcome. Only the statement text is logged, never its arguments, and the
// text is cut to queryLogText bytes; statements are code constants with
// placeholders, so no request value reaches the log.
type QueryLog struct {
	logger  *slog.Logger
	enabled atomic.Pointer[func() bool]
}

const queryLogText = 512

type queryLogKey struct{}

type queryLogStart struct {
	at  time.Time
	sql string
}

// NewQueryLog returns a switched-off log; SetEnabled connects the switch.
func NewQueryLog(logger *slog.Logger) *QueryLog { return &QueryLog{logger: logger} }

// SetEnabled installs the switch, normally the developer mode toggle.
func (q *QueryLog) SetEnabled(enabled func() bool) { q.enabled.Store(&enabled) }

func (q *QueryLog) on() bool {
	f := q.enabled.Load()
	return f != nil && (*f)()
}

func (q *QueryLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !q.on() {
		return ctx
	}
	return context.WithValue(ctx, queryLogKey{}, queryLogStart{at: time.Now(), sql: data.SQL})
}

func (q *QueryLog) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryLogKey{}).(queryLogStart)
	if !ok {
		return
	}
	text := strings.Join(strings.Fields(start.sql), " ")
	if len(text) > queryLogText {
		text = text[:queryLogText] + "…"
	}
	q.logger.Info("developer mode SQL log", "component", "db", "code", "devmode_sql_log", "statement", text,
		"durationMicros", time.Since(start.at).Microseconds(), "rows", data.CommandTag.RowsAffected(), "failed", data.Err != nil)
}

// OpenWithQueryLog is Open with the developer mode SQL log attached.
func OpenWithQueryLog(ctx context.Context, dsn string, maxConnections int32, log *QueryLog) (*Store, error) {
	return open(ctx, dsn, maxConnections, log)
}
