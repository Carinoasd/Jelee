package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestQueryLogThroughRouter runs the developer mode SQL log through the
// production log router: statements are readable while the toggle is on,
// secret text in them is masked, and nothing is written while it is off.
func TestQueryLogThroughRouter(t *testing.T) {
	var buf bytes.Buffer
	router, err := logging.Open(logging.Options{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = router.Close() }()
	var on atomic.Bool
	router.SetDeveloperLogging(on.Load, func() bool { return false })
	q := NewQueryLog(router.Logger())
	q.SetEnabled(on.Load)
	run := func(sql string) string {
		buf.Reset()
		ctx := q.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: sql})
		q.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("UPDATE 2")})
		if err := router.Flush(); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	const statement = "UPDATE accounts\n   SET name = $1 WHERE id = $2"
	if out := run(statement); out != "" {
		t.Fatalf("logged while off: %s", out)
	}
	on.Store(true)
	var rec struct {
		Code      string `json:"code"`
		Statement string `json:"statement"`
		Rows      int64  `json:"rows"`
		Failed    bool   `json:"failed"`
	}
	if err := json.Unmarshal([]byte(run(statement)), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Code != "devmode_sql_log" || rec.Statement != "UPDATE accounts SET name = $1 WHERE id = $2" || rec.Rows != 2 || rec.Failed {
		t.Fatalf("record: %+v", rec)
	}
	if out := run("ALTER ROLE app PASSWORD 'pw-secret-1'; SELECT 'postgres://u:pw-secret-2@h/db'"); strings.Contains(out, "pw-secret") || !strings.Contains(out, "ALTER ROLE app PASSWORD") {
		t.Fatalf("secret text: %s", out)
	}
	on.Store(false)
	if out := run(statement); out != "" {
		t.Fatalf("logged after switching off: %s", out)
	}
}

// TestSlowQueryLogThroughRouter: statements at or above the threshold are
// logged as templates through the production whitelist; literals and
// arguments never appear; a zero threshold attaches nothing.
func TestSlowQueryLogThroughRouter(t *testing.T) {
	var buf bytes.Buffer
	router, err := logging.Open(logging.Options{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = router.Close() }()
	q := NewQueryLog(router.Logger())
	run := func(sql string, wait time.Duration, failed error) string {
		buf.Reset()
		ctx := q.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: sql, Args: []any{"arg-secret-1"}})
		time.Sleep(wait)
		q.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("SELECT 3"), Err: failed})
		if err := router.Flush(); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	const statement = "SELECT id FROM sessions\n WHERE token_hash = 'literal-secret' AND user_id = $1"
	if out := run(statement, 5*time.Millisecond, nil); out != "" {
		t.Fatalf("logged without a threshold: %s", out)
	}
	if ctx := q.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: statement}); ctx.Value(queryLogKey{}) != nil {
		t.Fatal("tracer attached state with both logs off")
	}
	q.SetSlowThreshold(2 * time.Millisecond)
	if out := run(statement, 0, nil); out != "" {
		t.Fatalf("fast query logged: %s", out)
	}
	out := run(statement, 5*time.Millisecond, errors.New("boom"))
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatal(out)
	}
	if rec["code"] != "db_slow_query" || rec["sql"] != "SELECT id FROM sessions WHERE token_hash = ? AND user_id = $1" || rec["outcome"] != "failed" || rec["count"] != float64(3) ||
		rec["thresholdMs"] != float64(2) || rec["level"] != "WARN" || rec["durationMs"].(float64) < 2 {
		t.Fatalf("slow record %v", rec)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("literal or argument logged: %s", out)
	}
	q.SetSlowThreshold(-time.Second)
	if q.slow.Load() != 0 {
		t.Fatal("negative threshold kept")
	}
}
