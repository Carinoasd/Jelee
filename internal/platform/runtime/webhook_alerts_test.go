package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type alertOutbox struct {
	mu     sync.Mutex
	events []domain.WebhookEvent
}

func (o *alertOutbox) Append(_ context.Context, e domain.WebhookEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
	return nil
}

type noExpiry struct{}

func (noExpiry) RaiseExpiredSessions(context.Context, int) (int, error) { return 0, nil }

// Coded warnings and errors raise system.alert whatever the log level; the
// alert carries the code, component and severity only, and the record is
// still logged as configured.
func TestAlertHandlerRaisesCodedWarnings(t *testing.T) {
	var logged bytes.Buffer
	alerts := app.NewSystemAlerts(0, nil)
	logger := slog.New(newAlertHandler(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError}), alerts))
	logger.Warn("media probing is enabled but its isolated runtime is unavailable; probing stays disabled /srv/secret", "component", "probe", "code", "probe_runtime_unavailable", "reason", "/srv/secret")
	logger.Info("not an alert", "component", "probe", "code", "matroska_runtime_unavailable")
	logger.Warn("uncoded warning", "component", "http")
	logger.Warn("developer mode", "component", "devmode", "code", "devmode_active")
	logger.With("component", "matroska").Error("cleanup failed", "code", "matroska_runtime_unavailable")
	logger.WithGroup("nested").Warn("grouped code", "code", "log_prune_failed")
	outbox := &alertOutbox{}
	producers, err := app.NewWebhookProducers(outbox, alerts, noExpiry{}, app.WebhookProducerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = producers.FlushAlerts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(outbox.events) != 2 {
		t.Fatalf("alerts %+v", outbox.events)
	}
	probe, matroska := outbox.events[0].Data, outbox.events[1].Data
	if probe["code"] != "probe_runtime_unavailable" || probe["component"] != "probe" || probe["severity"] != "warning" ||
		matroska["code"] != "matroska_runtime_unavailable" || matroska["component"] != "matroska" || matroska["severity"] != "error" {
		t.Fatalf("alert data %v %v", probe, matroska)
	}
	raw, _ := json.Marshal(outbox.events)
	if strings.Contains(string(raw), "/srv") || strings.Contains(string(raw), "probing") {
		t.Fatalf("alert carries log text: %s", raw)
	}
	// The configured level still decides what is logged.
	if strings.Contains(logged.String(), "probing") || !strings.Contains(logged.String(), "cleanup failed") {
		t.Fatalf("logged %q", logged.String())
	}
	if newAlertHandler(slog.DiscardHandler, nil) != slog.DiscardHandler {
		t.Fatal("handler wrapped without alerts")
	}
}
