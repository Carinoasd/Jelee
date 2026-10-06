package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// TestAuditJanitorPurgesExpiredRowsPostgres runs the scheduled purge
// against the append-only guard: expired rows of each category leave in
// bounded batches through purge_audit_logs, rows inside retention and the
// purge records stay, a direct DELETE is still refused, and every pass that
// removed rows is logged through the production whitelist.
func TestAuditJanitorPurgesExpiredRowsPostgres(t *testing.T) {
	f := newAuditFixture(t)
	if _, err := f.s.SetAuditRetention(f.ctx, f.admin, 365, 90); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	keep := []int64{f.backdate(t, "user.updated", "audit", 300*day), f.backdate(t, "login.failed", "security", 80*day)}
	var expired []int64
	for i := 0; i < 3; i++ {
		expired = append(expired, f.backdate(t, "user.updated", "audit", time.Duration(366+i)*day), f.backdate(t, "login.failed", "security", time.Duration(91+i)*day))
	}
	var buf bytes.Buffer
	router, err := logging.Open(logging.Options{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = router.Close() }()
	janitor, err := app.NewAuditJanitor(f.s, app.AuditJanitorOptions{Interval: time.Hour, Batch: 2, MaxBatches: 2, Logger: router.Logger()})
	if err != nil {
		t.Fatal(err)
	}
	purgesBefore := f.count(t, `event='audit.retention_purged'`)
	// Six expired rows, batches of two, two batches per pass.
	first, err := janitor.Pass(f.ctx)
	if err != nil || !first.Limited || first.Batches != 2 || first.Audit+first.Security != 4 {
		t.Fatalf("first pass: %+v %v", first, err)
	}
	second, err := janitor.Pass(f.ctx)
	if err != nil || second.Limited || second.Audit+second.Security != 2 || first.Audit+second.Audit != 3 || first.Security+second.Security != 3 {
		t.Fatalf("second pass: %+v %v", second, err)
	}
	for _, id := range expired {
		if f.count(t, `id=$1`, id) != 0 {
			t.Fatalf("expired row %d survived", id)
		}
	}
	if f.count(t, `id=ANY($1)`, keep) != 2 {
		t.Fatal("a row inside retention was purged")
	}
	if n := f.count(t, `event='audit.retention_purged'`) - purgesBefore; n != 3 {
		t.Fatalf("purge batches audited %d times, want 3", n)
	}
	_, err = f.s.Pool.Exec(f.ctx, `DELETE FROM audit_logs WHERE id=$1`, keep[0])
	auditRejected(t, err, "direct delete beside the janitor")
	// The log line passes the production whitelist with its counts.
	janitor2, err := app.NewAuditJanitor(f.s, app.AuditJanitorOptions{Interval: time.Millisecond, Batch: 1, MaxBatches: 1, Logger: router.Logger()})
	if err != nil {
		t.Fatal(err)
	}
	f.backdate(t, "user.updated", "audit", 400*day)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		janitor2.Run(ctx)
	}()
	for f.count(t, `event='user.updated' AND occurred_at<now()-interval '399 days'`) != 0 {
		if ctx.Err() != nil {
			t.Fatal("scheduled pass did not run")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if err = router.Flush(); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil || !strings.HasPrefix(rec["msg"].(string), "audit retention purge") {
			continue
		}
		if rec["component"] != "gc" || rec["auditRows"] == logging.Redacted || rec["batches"] == logging.Redacted || rec["count"] == logging.Redacted {
			t.Fatalf("purge log fields redacted: %s", line)
		}
		found = found || rec["count"] == float64(1)
	}
	if !found {
		t.Fatalf("scheduled purge not logged: %s", buf.String())
	}
}
