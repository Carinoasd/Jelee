package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

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
