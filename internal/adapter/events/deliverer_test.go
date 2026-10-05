package events

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

type fakePoster struct {
	result outbound.PostResult
	err    error
	check  error
}

func (f fakePoster) CheckTarget(string) error { return f.check }

func (f fakePoster) Post(ctx context.Context, _ string, _ map[string]string, _ []byte, _ time.Duration) (outbound.PostResult, error) {
	if ctx.Err() != nil {
		return outbound.PostResult{}, ctx.Err()
	}
	return f.result, f.err
}

func TestDelivererClassifiesEveryResult(t *testing.T) {
	for _, tc := range []struct {
		name  string
		post  fakePoster
		want  domain.WebhookOutcome
		retry bool
	}{
		{"2xx", fakePoster{result: outbound.PostResult{Status: 204}}, domain.WebhookOutcome{Kind: domain.WebhookOutcomeDelivered, StatusCode: 204}, false},
		{"3xx", fakePoster{result: outbound.PostResult{Status: 302}}, domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 302}, false},
		{"404", fakePoster{result: outbound.PostResult{Status: 404}}, domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 404}, false},
		{"500", fakePoster{result: outbound.PostResult{Status: 500, RetryAfter: "30"}}, domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 500}, true},
		{"429", fakePoster{result: outbound.PostResult{Status: 429, RetryAfter: "30"}}, domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 429, RetryAfter: 30 * time.Second}, true},
		{"503 date", fakePoster{result: outbound.PostResult{Status: 503, RetryAfter: "Mon, 01 Jan 2001 00:00:00 GMT"}}, domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 503}, true},
		{"timeout", fakePoster{err: context.DeadlineExceeded}, domain.WebhookOutcome{Kind: domain.WebhookOutcomeTimeout}, true},
		{"denied", fakePoster{err: outbound.ErrDenied}, domain.WebhookOutcome{Kind: domain.WebhookOutcomeBlocked}, false},
		{"certificate", fakePoster{err: outbound.ErrCertificate}, domain.WebhookOutcome{Kind: domain.WebhookOutcomeTLS}, false},
		{"network", fakePoster{err: outbound.ErrUnavailable}, domain.WebhookOutcome{Kind: domain.WebhookOutcomeNetwork}, true},
	} {
		d, _ := NewDeliverer(tc.post)
		got, err := d.Deliver(context.Background(), app.WebhookRequest{URL: "https://hooks.example/", Timeout: time.Second})
		if err != nil || got != tc.want || got.Retryable() != tc.retry {
			t.Errorf("%s: %+v %v", tc.name, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, _ := NewDeliverer(fakePoster{})
	if _, err := d.Deliver(ctx, app.WebhookRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation became an outcome: %v", err)
	}
	denied, _ := NewDeliverer(fakePoster{check: outbound.ErrDenied})
	if err := denied.CheckWebhookTarget("http://x"); !errors.Is(err, domain.ErrWebhookTargetDenied) {
		t.Fatal(err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Duration{
		"": 0, "0": 0, "-5": 0, "120": 2 * time.Minute, "999999999": maxRetryAfter, "soon": 0,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		now.Add(-time.Hour).Format(http.TimeFormat):       0,
	} {
		if got := parseRetryAfter(value, now); got != want {
			t.Errorf("%q: %v want %v", value, got, want)
		}
	}
}
