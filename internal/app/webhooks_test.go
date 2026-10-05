package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

var webhookTestNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func webhookTestEndpoint(id string) WebhookEndpoint {
	return WebhookEndpoint{ID: id, URL: "https://hooks.example/jelee", Enabled: true, Timeout: DefaultWebhookTimeout,
		Retry: domain.DefaultWebhookRetryPolicy(), Headers: map[string]string{"X-Team": "media"}}
}

func webhookTestEvent(t *testing.T) domain.WebhookEvent {
	t.Helper()
	event, err := domain.NewWebhookEvent("evt-1", domain.WebhookScanCompleted, webhookTestNow,
		domain.WebhookSubject{Kind: domain.WebhookSubjectLibrary, ID: "lib-1"},
		map[string]any{"root": "/srv/media/<Movies>", "added": 3})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestPlanWebhookFanout(t *testing.T) {
	event := webhookTestEvent(t)
	all := webhookTestEndpoint("all")
	scans := webhookTestEndpoint("scans")
	scans.Filter = []domain.WebhookEventType{domain.WebhookScanCompleted}
	logins := webhookTestEndpoint("logins")
	logins.Filter = []domain.WebhookEventType{domain.WebhookUserLogin}
	disabled := webhookTestEndpoint("disabled")
	disabled.Enabled = false
	got := PlanWebhookFanout(event, []WebhookEndpoint{all, scans, logins, disabled})
	if strings.Join(got, ",") != "all,scans" {
		t.Fatalf("fanout %v", got)
	}
}

func TestBuildWebhookRequestSignsAndProtectsHeaders(t *testing.T) {
	keys := domain.WebhookSigningKeys{Current: domain.WebhookSecret(bytes.Repeat([]byte{'k'}, 32))}
	event := webhookTestEvent(t)
	req, err := BuildWebhookRequest(webhookTestEndpoint("e"), keys, event, webhookTestNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if req.Headers["X-Team"] != "media" || req.Headers["Content-Type"] != "application/json" || req.Headers[domain.WebhookEventIDHeader] != "evt-1" {
		t.Fatalf("headers %v", req.Headers)
	}
	signed := domain.WebhookSignedHeaders{Timestamp: req.Headers[domain.WebhookTimestampHeader], Signature: req.Headers[domain.WebhookSignatureHeader]}
	// Signed with the attempt time, not occurredAt, so retries stay verifiable.
	if err := domain.VerifyWebhookSignature([]domain.WebhookSecret{keys.Current}, signed, req.Body, webhookTestNow.Add(time.Hour), 0); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if bytes.Contains(req.Body, []byte("/srv")) || !bytes.Contains(req.Body, []byte(`"root":"<Movies>"`)) {
		t.Fatalf("body %s", req.Body)
	}
	var back domain.WebhookEvent
	if err := json.Unmarshal(req.Body, &back); err != nil || back.EventID != "evt-1" || back.Type != domain.WebhookScanCompleted {
		t.Fatalf("body decode %v %+v", err, back)
	}

	bad := webhookTestEndpoint("e")
	bad.Headers = map[string]string{domain.WebhookSignatureHeader: "forged"}
	if _, err := BuildWebhookRequest(bad, keys, event, webhookTestNow); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("reserved header accepted: %v", err)
	}
	slow := webhookTestEndpoint("e")
	slow.Timeout = time.Minute
	if _, err := BuildWebhookRequest(slow, keys, event, webhookTestNow); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("timeout accepted: %v", err)
	}
	if _, err := BuildWebhookRequest(webhookTestEndpoint("e"), domain.WebhookSigningKeys{}, event, webhookTestNow); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing key accepted: %v", err)
	}
	leaky := event
	leaky.Data = map[string]any{"token": "abc"}
	if _, err := BuildWebhookRequest(webhookTestEndpoint("e"), keys, leaky, webhookTestNow); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unredacted event encoded: %v", err)
	}
	big := event
	big.Data = map[string]any{}
	for i := 0; i < 40; i++ {
		big.Data[strings.Repeat("k", 10)+string(rune('A'+i))] = strings.Repeat("v", 2000)
	}
	if _, err := EncodeWebhookEvent(big); !errors.Is(err, errWebhookBodyTooLarge) {
		t.Fatalf("oversized body: %v", err)
	}
}

func TestResolveWebhookAttempt(t *testing.T) {
	policy := domain.WebhookRetryPolicy{MaxAttempts: 3, BaseDelay: 10 * time.Second, MaxDelay: time.Minute, Jitter: 0}
	d := WebhookDelivery{ID: "d1", EndpointID: "e", Event: webhookTestEvent(t)}
	failure := domain.WebhookOutcome{Kind: domain.WebhookOutcomeHTTP, StatusCode: 502}
	at := webhookTestNow
	var states []string
	for {
		rec := ResolveWebhookAttempt(d, policy, failure, at, at.Add(time.Second), nil)
		if rec.Attempt != d.Attempts+1 || rec.DeliveryID != "d1" {
			t.Fatalf("record %+v", rec)
		}
		states = append(states, string(rec.Decision.State)+"/"+rec.Decision.Delay.String())
		if rec.Decision.State != domain.WebhookDeliveryPending {
			break
		}
		d.Attempts++
		at = rec.Decision.NextAt
	}
	if got := strings.Join(states, " "); got != "pending/10s pending/20s dead/0s" {
		t.Fatalf("states %s", got)
	}
}
