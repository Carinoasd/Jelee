package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/adapter/events"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/MoYuanCN/Jelee/internal/platform/secretbox"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
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
	if dev := lifetime.dev; dev != nil && dev.Capable() {
		// G45.4 relax_ssrf_strict: private and loopback receivers while the
		// toggle is on, for local test endpoints.
		client.AllowPrivateTargets(func() bool { return dev.Effective(devmode.RelaxSSRFStrict) })
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
		Spans: webhookSpans,
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

// webhookSpans starts the span of one delivery attempt (G46.6). It joins the
// trace of the change that raised the event when this process remembers it.
func webhookSpans(ctx context.Context, eventID string) (context.Context, func(string)) {
	ctx, span := tracing.Default().StartLinked(ctx, "webhook.deliver", "webhook", tracing.LinkWebhookEvent, eventID)
	return ctx, span.End
}

// twoFactorBox seals authenticator secrets (G07.8) with the same master key
// as webhook secrets, which works whether or not webhooks are enabled. The
// sealing context binds each secret to its user and purpose, so a webhook
// value can never open as a secret or the reverse. Without a usable key the
// second factor cannot be enrolled; the server still starts, and enrolled
// accounts still need their second step (recovery codes complete it).
func twoFactorBox(c config.Config, l *slog.Logger) app.SecretBox {
	if strings.TrimSpace(string(c.Webhooks.MasterKey)) == "" {
		return nil
	}
	key, err := c.Webhooks.Key()
	if err != nil {
		l.Warn("two-factor authentication unavailable: the master key is invalid", "component", "accounts", "code", "two_factor_key_invalid")
		return nil
	}
	box, err := secretbox.New(key)
	clear(key)
	if err != nil {
		l.Warn("two-factor authentication unavailable: the master key is invalid", "component", "accounts", "code", "two_factor_key_invalid")
		return nil
	}
	return box
}
