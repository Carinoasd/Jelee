package runtime

import (
	"errors"
	"log/slog"

	"github.com/MoYuanCN/Jelee/internal/adapter/events"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/MoYuanCN/Jelee/internal/platform/secretbox"
)

// newWebhooks builds the G12 administration service and the background
// deliverer. It returns nil, nil when webhooks are disabled. Without a
// usable master key, configuration validation has already refused to start.
func newWebhooks(c config.Config, store *postgres.Store, budget *resources.Budget, l *slog.Logger, lifetime *lifetime) (*app.Webhooks, error) {
	if !c.EnableWebhooks {
		store.EnableWebhookEvents(false)
		return nil, nil
	}
	if err := c.Webhooks.Validate(); err != nil {
		return nil, err
	}
	key, err := c.Webhooks.Key()
	if err != nil {
		return nil, err
	}
	box, err := secretbox.New(key)
	clear(key)
	if err != nil {
		return nil, errors.New("invalid webhook master key")
	}
	roots, err := c.Webhooks.RootCAs()
	if err != nil {
		return nil, err
	}
	client, err := outbound.NewWebhookClient(c.Webhooks.AllowedHosts, roots, budget)
	if err != nil {
		return nil, errors.New("cannot build the webhook client")
	}
	deliverer, err := events.NewDeliverer(client)
	if err != nil {
		return nil, err
	}
	service, err := app.NewWebhooks(store, box, deliverer, deliverer, app.WebhookOptions{})
	if err != nil {
		return nil, err
	}
	p := c.Webhooks
	dispatcher, err := app.NewWebhookDispatcher(store, store, box, deliverer, app.WebhookDispatcherOptions{
		Batch: p.BatchSize(), Concurrency: p.Workers(), Lease: p.Lease(), PollInterval: p.PollInterval(), Retention: p.Retention(), Logger: l,
	})
	if err != nil {
		return nil, err
	}
	lifetime.webhooks = dispatcher
	lifetime.closeWebhookClient = client.CloseIdleConnections
	// Producers append events only from here on.
	store.EnableWebhookEvents(true)
	return service, nil
}
