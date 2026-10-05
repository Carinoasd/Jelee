// Package events delivers webhook requests (G12) through the SSRF-guarded
// outbound client and classifies every result into a delivery outcome.
package events

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

// Poster is the part of *outbound.Client the deliverer uses.
type Poster interface {
	CheckTarget(rawURL string) error
	Post(ctx context.Context, rawURL string, headers map[string]string, body []byte, timeout time.Duration) (outbound.PostResult, error)
}

// Deliverer implements app.WebhookDeliverer and app.WebhookTargetPolicy.
type Deliverer struct{ client Poster }

var (
	_ app.WebhookDeliverer    = (*Deliverer)(nil)
	_ app.WebhookTargetPolicy = (*Deliverer)(nil)
)

func NewDeliverer(client Poster) (*Deliverer, error) {
	if client == nil {
		return nil, errors.New("webhook client must be provided")
	}
	return &Deliverer{client: client}, nil
}

func (d *Deliverer) CheckWebhookTarget(rawURL string) error {
	if d.client.CheckTarget(rawURL) != nil {
		return domain.ErrWebhookTargetDenied
	}
	return nil
}

// maxRetryAfter bounds a Retry-After hint; the retry policy caps it again.
const maxRetryAfter = 24 * time.Hour

// Deliver sends req once. Only cancellation of ctx is returned as an error;
// every other failure is an outcome.
func (d *Deliverer) Deliver(ctx context.Context, req app.WebhookRequest) (domain.WebhookOutcome, error) {
	result, err := d.client.Post(ctx, req.URL, req.Headers, req.Body, req.Timeout)
	if err != nil {
		if ctx.Err() != nil {
			return domain.WebhookOutcome{}, ctx.Err()
		}
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return domain.WebhookOutcome{Kind: domain.WebhookOutcomeTimeout}, nil
		case errors.Is(err, outbound.ErrDenied), errors.Is(err, outbound.ErrRedirect):
			return domain.WebhookOutcome{Kind: domain.WebhookOutcomeBlocked}, nil
		case errors.Is(err, outbound.ErrCertificate):
			return domain.WebhookOutcome{Kind: domain.WebhookOutcomeTLS}, nil
		default:
			return domain.WebhookOutcome{Kind: domain.WebhookOutcomeNetwork}, nil
		}
	}
	if result.Status >= 200 && result.Status <= 299 {
		return domain.WebhookOutcome{Kind: domain.WebhookOutcomeDelivered, StatusCode: result.Status}, nil
	}
	outcome := domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: result.Status}
	if result.Status == http.StatusTooManyRequests || result.Status == http.StatusServiceUnavailable {
		outcome.RetryAfter = parseRetryAfter(result.RetryAfter, time.Now())
	}
	return outcome, nil
}

// parseRetryAfter accepts delta seconds or an HTTP date.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		d := at.Sub(now)
		if d <= 0 {
			return 0
		}
		return min(d, maxRetryAfter)
	}
	return 0
}
