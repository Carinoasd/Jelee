package runtime

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// lifetimeWebhookStore notices any use after the pool closed.
type lifetimeWebhookStore struct {
	closed  *atomic.Int32
	claims  atomic.Int32
	misused atomic.Bool
}

func (s *lifetimeWebhookStore) use() {
	if s.closed.Load() != 0 {
		s.misused.Store(true)
	}
}
func (s *lifetimeWebhookStore) ListDeliveryEndpoints(context.Context) ([]app.WebhookEndpoint, error) {
	s.use()
	return nil, nil
}
func (s *lifetimeWebhookStore) LoadWebhookEndpoint(context.Context, string) (app.WebhookSealedEndpoint, error) {
	s.use()
	return app.WebhookSealedEndpoint{}, domain.ErrNotFound
}
func (s *lifetimeWebhookStore) FetchUnplanned(context.Context, int) ([]domain.WebhookEvent, error) {
	s.use()
	return nil, nil
}
func (s *lifetimeWebhookStore) CreateDeliveries(context.Context, domain.WebhookEvent, []string, time.Time) error {
	s.use()
	return nil
}
func (s *lifetimeWebhookStore) ClaimDue(context.Context, time.Time, time.Time, int) ([]app.WebhookDelivery, error) {
	s.use()
	s.claims.Add(1)
	return nil, nil
}
func (s *lifetimeWebhookStore) RecordAttempt(context.Context, app.WebhookAttemptRecord) error {
	s.use()
	return nil
}
func (s *lifetimeWebhookStore) PurgeWebhookHistory(context.Context, time.Time, int) (int, error) {
	s.use()
	return 0, nil
}

type lifetimeBox struct{}

func (lifetimeBox) Seal(string, []byte) ([]byte, error) { return nil, nil }
func (lifetimeBox) Open(string, []byte) ([]byte, error) { return nil, nil }

type lifetimeDeliverer struct{}

func (lifetimeDeliverer) Deliver(context.Context, app.WebhookRequest) (domain.WebhookOutcome, error) {
	return domain.WebhookOutcome{Kind: domain.WebhookOutcomeDelivered}, nil
}

// The deliverer starts with the workers, is cancelled with the lifetime
// and joined before the pool closes.
func TestWebhookDispatcherJoinsBeforePoolCloses(t *testing.T) {
	l, a, closed, _ := testLifetime(t, nil, http.NotFoundHandler())
	store := &lifetimeWebhookStore{closed: closed}
	dispatcher, err := app.NewWebhookDispatcher(store, store, lifetimeBox{}, lifetimeDeliverer{}, app.WebhookDispatcherOptions{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	l.webhooks = dispatcher
	var clientClosed atomic.Int32
	l.closeWebhookClient = func() {
		if closed.Load() != 0 {
			t.Error("webhook client closed after the pool")
		}
		clientClosed.Add(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for store.claims.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("dispatcher did not run")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err = a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	waitChannel(t, l.webhooksDone)
	if closed.Load() != 1 || clientClosed.Load() != 1 || store.misused.Load() {
		t.Fatalf("closed=%d client=%d misused=%t", closed.Load(), clientClosed.Load(), store.misused.Load())
	}
}

func TestNewWebhooksRequiresMasterKeyAndEnablesEvents(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 2, Queue: 4})
	if err != nil {
		t.Fatal(err)
	}
	store := &postgres.Store{}
	cfg := config.Config{EnableAccounts: true}
	l := newLifetime(logger)
	if service, err := newWebhooks(cfg, store, budget, logger, l); service != nil || err != nil || l.webhooks != nil {
		t.Fatal("disabled webhooks built a service")
	}
	cfg.EnableWebhooks, cfg.Webhooks = true, config.DefaultWebhooksConfig()
	if _, err := newWebhooks(cfg, store, budget, logger, l); err == nil || !strings.Contains(err.Error(), "JELEE_WEBHOOK_MASTER_KEY") {
		t.Fatalf("missing master key: %v", err)
	}
	cfg.Webhooks.MasterKey = config.MasterKey(strings.Repeat("ab", 32))
	service, err := newWebhooks(cfg, store, budget, logger, l)
	if err != nil || service == nil || l.webhooks == nil || l.closeWebhookClient == nil {
		t.Fatalf("enabled webhooks: %v", err)
	}
	cfg.Webhooks.CAFile = "/nonexistent/jelee-ca.pem"
	if _, err := newWebhooks(cfg, store, budget, logger, newLifetime(logger)); err == nil {
		t.Fatal("unreadable CA file accepted")
	}
}
