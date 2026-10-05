package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type recordingAdminRepo struct {
	WebhookAdminRepository
	created, updated WebhookEndpointRecord
	rotated          []byte
	until            time.Time
	sealed           WebhookSealedEndpoint
}

func (r *recordingAdminRepo) CreateWebhook(_ context.Context, _ domain.Actor, rec WebhookEndpointRecord) (WebhookEndpointView, error) {
	r.created = rec
	r.sealed = WebhookSealedEndpoint{Endpoint: WebhookEndpoint{ID: rec.ID, URL: rec.URL, Enabled: rec.Enabled, Filter: rec.Events, Timeout: rec.Timeout, Retry: rec.Retry},
		Keys: WebhookSealedKeys{Current: rec.SealedSecret}, Headers: rec.SealedHeaders}
	return WebhookEndpointView{ID: rec.ID}, nil
}

func (r *recordingAdminRepo) UpdateWebhook(_ context.Context, _ domain.Actor, rec WebhookEndpointRecord) (WebhookEndpointView, error) {
	r.updated = rec
	return WebhookEndpointView{ID: rec.ID}, nil
}

func (r *recordingAdminRepo) RotateWebhookSecret(_ context.Context, _ domain.Actor, id string, sealed []byte, until time.Time) (WebhookEndpointView, error) {
	r.rotated, r.until = sealed, until
	return WebhookEndpointView{ID: id}, nil
}

func (r *recordingAdminRepo) WebhookForTest(context.Context, domain.Actor, string) (WebhookSealedEndpoint, error) {
	return r.sealed, nil
}

type prefixTarget struct{}

func (prefixTarget) CheckWebhookTarget(raw string) error {
	if !strings.HasPrefix(raw, "https://") {
		return domain.ErrWebhookTargetDenied
	}
	return nil
}

func TestWebhookAdministrationSealsSecretsAndHeaders(t *testing.T) {
	repo := &recordingAdminRepo{}
	deliverer := &scriptedDeliverer{}
	clock := &stepClock{t: webhookTestNow}
	w, err := NewWebhooks(repo, prefixBox{}, prefixTarget{}, deliverer, WebhookOptions{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	actor := domain.Actor{UserID: "u", SessionID: "s"}
	view, secret, err := w.Create(context.Background(), actor, WebhookEndpointInput{Name: "Ops", URL: "https://hooks.example/a", Enabled: true,
		Events: []domain.WebhookEventType{domain.WebhookScanFailed, domain.WebhookMediaAdded, domain.WebhookScanFailed}, Headers: map[string]string{"X-B": "2", "X-A": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := repo.created
	if !domain.ValidID(view.ID) || !strings.HasPrefix(secret, "whsec_") || len(secret) != 49 {
		t.Fatalf("created %q %q", view.ID, secret)
	}
	if strings.Join(rec.HeaderNames, ",") != "X-A,X-B" || len(rec.Events) != 2 || rec.Timeout != DefaultWebhookTimeout || rec.Retry != domain.DefaultWebhookRetryPolicy() || !rec.ReplaceHeaders {
		t.Fatalf("record %+v", rec)
	}
	// Sealed under contexts bound to this endpoint.
	if string(rec.SealedSecret) != WebhookSecretContext(view.ID)+"|"+secret || !strings.HasPrefix(string(rec.SealedHeaders), WebhookHeadersContext(view.ID)+"|") {
		t.Fatal("secret or headers not sealed for this endpoint")
	}
	// The test send signs with the stored secret and the unsealed headers.
	result, err := w.Test(context.Background(), actor, view.ID)
	if err != nil || result.Outcome != domain.WebhookOutcomeDelivered || !strings.HasPrefix(result.EventID, "test-") {
		t.Fatalf("test send %+v %v", result, err)
	}
	r := deliverer.requests[0]
	signed := domain.WebhookSignedHeaders{Timestamp: r.Headers[domain.WebhookTimestampHeader], Signature: r.Headers[domain.WebhookSignatureHeader]}
	if err := domain.VerifyWebhookSignature([]domain.WebhookSecret{domain.WebhookSecret(secret)}, signed, r.Body, webhookTestNow, 0); err != nil || r.Headers["X-A"] != "1" {
		t.Fatalf("test request %v %v", err, r.Headers)
	}
	if !strings.Contains(string(r.Body), `"type":"system.alert"`) || !strings.Contains(string(r.Body), `"test":true`) {
		t.Fatalf("test body %s", r.Body)
	}

	// Replace without headers keeps them; an empty map removes them.
	if _, err = w.Update(context.Background(), actor, view.ID, WebhookEndpointInput{Name: "Ops", URL: "https://hooks.example/b"}); err != nil || repo.updated.ReplaceHeaders {
		t.Fatalf("keep headers %+v %v", repo.updated, err)
	}
	if _, err = w.Update(context.Background(), actor, view.ID, WebhookEndpointInput{Name: "Ops", URL: "https://hooks.example/b", Headers: map[string]string{}}); err != nil || !repo.updated.ReplaceHeaders || repo.updated.SealedHeaders != nil {
		t.Fatalf("remove headers %+v %v", repo.updated, err)
	}

	newView, rotated, err := w.Rotate(context.Background(), actor, view.ID, time.Hour)
	if err != nil || rotated == secret || newView.ID != view.ID || !repo.until.Equal(webhookTestNow.Add(time.Hour)) || string(repo.rotated) != WebhookSecretContext(view.ID)+"|"+rotated {
		t.Fatalf("rotate %v %v", err, repo.until)
	}
	if _, _, err = w.Rotate(context.Background(), actor, view.ID, 0); err != nil || !repo.until.IsZero() {
		t.Fatalf("rotate without grace %v", err)
	}

	for name, in := range map[string]WebhookEndpointInput{
		"plain http":      {Name: "x", URL: "http://hooks.example/a"},
		"empty name":      {Name: "", URL: "https://hooks.example/a"},
		"padded name":     {Name: " x", URL: "https://hooks.example/a"},
		"control name":    {Name: "a\nb", URL: "https://hooks.example/a"},
		"unknown event":   {Name: "x", URL: "https://hooks.example/a", Events: []domain.WebhookEventType{"media.exploded"}},
		"reserved header": {Name: "x", URL: "https://hooks.example/a", Headers: map[string]string{"X-Jelee-Signature": "v1=forged"}},
		"slow":            {Name: "x", URL: "https://hooks.example/a", TimeoutSeconds: 31},
		"retry":           {Name: "x", URL: "https://hooks.example/a", Retry: &WebhookRetryView{MaxAttempts: 21, BaseDelaySeconds: 1, MaxDelaySeconds: 2}},
	} {
		if _, _, err := w.Create(context.Background(), actor, in); err == nil {
			t.Errorf("%s accepted", name)
		} else if name == "plain http" && !errors.Is(err, domain.ErrWebhookTargetDenied) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := w.Deliveries(context.Background(), actor, view.ID, "lost", "", 10); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown state accepted: %v", err)
	}
	if _, err := w.Replay(context.Background(), actor, "not-an-id", view.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("malformed id: %v", err)
	}
}

// OpenWebhookEndpoint refuses values sealed for another endpoint, so a
// secret copied between rows cannot sign for the wrong endpoint.
func TestOpenWebhookEndpointBindsContext(t *testing.T) {
	box := prefixBox{}
	secret, _ := box.Seal(WebhookSecretContext("a"), []byte(strings.Repeat("k", 40)))
	if _, _, err := OpenWebhookEndpoint(box, WebhookSealedEndpoint{Endpoint: WebhookEndpoint{ID: "b"}, Keys: WebhookSealedKeys{Current: secret}}); !ErrWebhookSealed(err) {
		t.Fatalf("foreign secret opened: %v", err)
	}
	headers, _ := box.Seal(WebhookSecretContext("a"), []byte(`{}`))
	if _, _, err := OpenWebhookEndpoint(box, WebhookSealedEndpoint{Endpoint: WebhookEndpoint{ID: "a"}, Keys: WebhookSealedKeys{Current: secret}, Headers: headers}); !ErrWebhookSealed(err) {
		t.Fatalf("headers sealed as a secret opened: %v", err)
	}
	if strings.Contains(errWebhookSealed.Error(), "kkkk") {
		t.Fatal("error leaks material")
	}
}
