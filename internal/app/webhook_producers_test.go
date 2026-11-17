package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type recordingOutbox struct {
	mu     sync.Mutex
	events []domain.WebhookEvent
	fail   error
}

func (o *recordingOutbox) Append(_ context.Context, e domain.WebhookEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail != nil {
		return o.fail
	}
	if err := e.Validate(); err != nil {
		return err
	}
	o.events = append(o.events, e)
	return nil
}

type countingExpiry struct {
	calls  int
	pages  []int
	limits []int
}

func (c *countingExpiry) RaiseExpiredSessions(_ context.Context, limit int) (int, error) {
	c.calls++
	c.limits = append(c.limits, limit)
	if len(c.pages) == 0 {
		return 0, nil
	}
	n := c.pages[0]
	c.pages = c.pages[1:]
	return n, nil
}

func TestSystemAlertCodes(t *testing.T) {
	for _, code := range []string{"probe_runtime_unavailable", "matroska_runtime_unavailable", "subtitle_ocr_tool_missing", "embedded_cover_runtime_unavailable",
		"chapter_image_prerequisite_unavailable", "schedule_dispatch_failed", "watch_unavailable"} {
		if !SystemAlertCode(code) {
			t.Errorf("%s does not alert", code)
		}
	}
	// Request-level, developer and noisy codes never alert, nor does a code
	// that is not a fixed machine code.
	for _, code := range []string{"", "matroska_", "devmode_active", "db_slow_query", "client_blocked", "scan_unavailable", "probe_runtime_unavailable /srv/x", "Matroska_x", strings.Repeat("a", 65)} {
		if SystemAlertCode(code) {
			t.Errorf("%q alerts", code)
		}
	}
}

// G12.1 system.alert: one event per code per interval, holding only the
// code, component and severity; a failed append is retried, and the queue is
// bounded while nothing drains it.
func TestSystemAlertsThrottleAndAppend(t *testing.T) {
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	alerts := NewSystemAlerts(time.Hour, func() time.Time { return now })
	if !alerts.Raise(SystemAlert{Code: "probe_runtime_unavailable", Component: "probe", Severity: "warning"}) {
		t.Fatal("first alert refused")
	}
	if alerts.Raise(SystemAlert{Code: "probe_runtime_unavailable", Component: "probe", Severity: "warning"}) {
		t.Fatal("repeated alert within the interval accepted")
	}
	if alerts.Raise(SystemAlert{Code: "devmode_active", Severity: "warning"}) || alerts.Raise(SystemAlert{Code: "watch_unavailable", Severity: "info"}) {
		t.Fatal("unknown code or severity accepted")
	}
	// A component that is not a fixed label is dropped, not forwarded.
	if !alerts.Raise(SystemAlert{Code: "matroska_runtime_unavailable", Component: "/srv/media", Severity: "error"}) {
		t.Fatal("second code refused")
	}
	outbox := &recordingOutbox{fail: domain.ErrDatabase}
	producers, err := NewWebhookProducers(outbox, alerts, &countingExpiry{}, WebhookProducerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = producers.FlushAlerts(ctx); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal(err)
	}
	outbox.fail = nil
	if err = producers.FlushAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if len(outbox.events) != 2 {
		t.Fatalf("appended %d alerts after retry", len(outbox.events))
	}
	first, second := outbox.events[0], outbox.events[1]
	if first.Type != domain.WebhookSystemAlert || first.Subject.Kind != domain.WebhookSubjectSystem || first.Subject.ID != "" || !first.OccurredAt.Equal(now) ||
		first.Data["code"] != "probe_runtime_unavailable" || first.Data["component"] != "probe" || first.Data["severity"] != "warning" || len(first.Data) != 3 {
		t.Fatalf("first alert %+v", first)
	}
	if _, ok := second.Data["component"]; ok || second.Data["severity"] != "error" {
		t.Fatalf("second alert %+v", second)
	}
	raw, _ := json.Marshal(outbox.events)
	if strings.Contains(string(raw), "/srv") || first.EventID == second.EventID {
		t.Fatalf("alerts %s", raw)
	}
	// After the interval the code alerts again.
	now = now.Add(time.Hour)
	if !alerts.Raise(SystemAlert{Code: "probe_runtime_unavailable", Component: "probe", Severity: "warning"}) {
		t.Fatal("alert after the interval refused")
	}
	// The queue is bounded while nothing drains it.
	bounded := NewSystemAlerts(time.Nanosecond, func() time.Time { now = now.Add(time.Second); return now })
	accepted := 0
	for range 2 * maxPendingAlerts {
		if bounded.Raise(SystemAlert{Code: "log_prune_failed", Severity: "warning"}) {
			accepted++
		}
	}
	if accepted != maxPendingAlerts {
		t.Fatalf("queue accepted %d", accepted)
	}
}

// The expiry sweep repeats full pages and stops at the first short one.
func TestWebhookProducersSweepExpiryPages(t *testing.T) {
	expiry := &countingExpiry{pages: []int{3, 3, 1, 3}}
	producers, err := NewWebhookProducers(&recordingOutbox{}, NewSystemAlerts(0, nil), expiry, WebhookProducerOptions{ExpiryBatch: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err = producers.SweepExpiry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if expiry.calls != 3 || expiry.limits[0] != 3 {
		t.Fatalf("sweep calls %d limits %v", expiry.calls, expiry.limits)
	}
	if _, err = NewWebhookProducers(nil, NewSystemAlerts(0, nil), expiry, WebhookProducerOptions{}); err == nil {
		t.Fatal("nil outbox accepted")
	}
}
