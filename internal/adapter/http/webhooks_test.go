package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/secretbox"
)

// testWebhookMasterKey is a fixed test-only master key (32 bytes, hex).
const testWebhookMasterKey = "8f3c2a1b4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"

type stubWebhookRepository struct{ app.WebhookAdminRepository }

func (stubWebhookRepository) ListWebhooks(context.Context, domain.Actor) ([]app.WebhookEndpointView, error) {
	return []app.WebhookEndpointView{}, nil
}

// httpTarget allows any https URL; the outbound policy has its own tests.
type httpTarget struct{}

func (httpTarget) CheckWebhookTarget(raw string) error {
	if !strings.HasPrefix(raw, "https://") || strings.Contains(raw, "127.0.0.1") {
		return domain.ErrWebhookTargetDenied
	}
	return nil
}

type httpDeliverer struct{ requests []app.WebhookRequest }

func (d *httpDeliverer) Deliver(_ context.Context, r app.WebhookRequest) (domain.WebhookOutcome, error) {
	d.requests = append(d.requests, r)
	return domain.WebhookOutcome{Kind: domain.WebhookOutcomeDelivered, StatusCode: 204}, nil
}

func testWebhookBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i * 7)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func httpWebhooks(t *testing.T, repo app.WebhookAdminRepository) *app.Webhooks {
	t.Helper()
	w, err := app.NewWebhooks(repo, testWebhookBox(t), httpTarget{}, &httpDeliverer{}, app.WebhookOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func webhookHTTP(t *testing.T, handler http.Handler, method, path, token string, body any) (int, map[string]any, string) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, "http://localhost"+path, reader)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var envelope map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	return w.Code, envelope, w.Body.String()
}

// TestWebhookAdministrationHTTPPostgres drives the administration API
// against PostgreSQL: the secret is shown once and stored sealed, headers
// are never returned, viewers are refused, the target policy is enforced,
// changes are audited and a replay of a pending delivery conflicts.
func TestWebhookAdministrationHTTPPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	admin, err := store.Provision(ctx, "hook-admin", access.ClientNative, true)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Provision(ctx, "hook-viewer", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := leakHandler(t, store, leakConfig(t, dsn, 0))
	input := map[string]any{"name": "Ops", "url": "https://hooks.example/jelee?team=media", "events": []string{"scan.completed", "media.added"},
		"headers": map[string]string{"X-Team": "media-header-value-Qz9"}, "retry": map[string]any{"maxAttempts": 3, "baseDelaySeconds": 5, "maxDelaySeconds": 60, "jitter": 0.1}}
	if status, _, _ := webhookHTTP(t, handler, http.MethodPost, "/api/v1/webhooks", viewer, input); status != http.StatusForbidden {
		t.Fatalf("viewer created a webhook: %d", status)
	}
	status, created, raw := webhookHTTP(t, handler, http.MethodPost, "/api/v1/webhooks", admin, input)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	data := created["data"].(map[string]any)
	secret, _ := data["secret"].(string)
	hook := data["webhook"].(map[string]any)
	id := hook["id"].(string)
	if !strings.HasPrefix(secret, "whsec_") || len(secret) < 40 || hook["enabled"] != true || strings.Contains(raw, "media-header-value-Qz9") {
		t.Fatalf("create response %s", raw)
	}
	if names := hook["headerNames"].([]any); len(names) != 1 || names[0] != "X-Team" {
		t.Fatalf("header names %v", names)
	}
	// The secret and header values are stored sealed only.
	var plain int
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM webhooks WHERE position($1::bytea IN secret_sealed)>0 OR position($2::bytea IN headers_sealed)>0`, []byte(secret), []byte("media-header-value-Qz9")).Scan(&plain); err != nil || plain != 0 {
		t.Fatalf("plain secret stored: %d %v", plain, err)
	}
	for _, path := range []string{"/api/v1/webhooks", "/api/v1/webhooks/" + id} {
		status, _, body := webhookHTTP(t, handler, http.MethodGet, path, admin, nil)
		if status != http.StatusOK || strings.Contains(body, secret) || strings.Contains(body, "media-header-value-Qz9") || strings.Contains(body, "secret_sealed") {
			t.Fatalf("%s: %d %s", path, status, body)
		}
	}
	for _, url := range []string{"http://hooks.example/jelee", "https://127.0.0.1/hook"} {
		bad := map[string]any{"name": "Bad", "url": url}
		if status, envelope, _ := webhookHTTP(t, handler, http.MethodPost, "/api/v1/webhooks", admin, bad); status != http.StatusBadRequest || envelope["error"].(map[string]any)["code"] != "webhook_target_denied" {
			t.Fatalf("%s accepted: %d", url, status)
		}
	}
	// Replace without headers keeps them; disabling is part of replace.
	update := map[string]any{"name": "Ops", "url": "https://hooks.example/v2", "enabled": false}
	status, updated, raw := webhookHTTP(t, handler, http.MethodPut, "/api/v1/webhooks/"+id, admin, update)
	if status != http.StatusOK || updated["data"].(map[string]any)["enabled"] != false || len(updated["data"].(map[string]any)["headerNames"].([]any)) != 1 {
		t.Fatalf("update: %d %s", status, raw)
	}
	status, rotated, raw := webhookHTTP(t, handler, http.MethodPost, "/api/v1/webhooks/"+id+"/rotate-secret", admin, map[string]any{"graceSeconds": 3600})
	if status != http.StatusOK || rotated["data"].(map[string]any)["secret"] == secret || rotated["data"].(map[string]any)["webhook"].(map[string]any)["previousSecretUntil"] == nil {
		t.Fatalf("rotate: %d %s", status, raw)
	}
	// Test send of a disabled endpoint goes straight to the deliverer.
	status, tested, raw := webhookHTTP(t, handler, http.MethodPost, "/api/v1/webhooks/"+id+"/test", admin, map[string]any{})
	if status != http.StatusOK || tested["data"].(map[string]any)["outcome"] != "delivered" {
		t.Fatalf("test: %d %s", status, raw)
	}
	// A pending delivery cannot be replayed; a dead one can, once.
	var delivery string
	if err = store.Pool.QueryRow(ctx, `WITH o AS (INSERT INTO webhook_outbox(event_type,occurred_at,subject_kind,subject_id,planned_at) VALUES('scan.completed',now(),'library','lib',now()) RETURNING id)
 INSERT INTO webhook_deliveries(outbox_id,webhook_id,next_attempt_at) SELECT o.id,$1::uuid,now() FROM o RETURNING id::text`, id).Scan(&delivery); err != nil {
		t.Fatal(err)
	}
	replay := "/api/v1/webhooks/" + id + "/deliveries/" + delivery + "/replay"
	if status, _, _ := webhookHTTP(t, handler, http.MethodPost, replay, admin, map[string]any{}); status != http.StatusConflict {
		t.Fatalf("pending replayed: %d", status)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE webhook_deliveries SET state='dead',next_attempt_at=NULL,attempts=3,last_outcome='http',last_status=500 WHERE id=$1::uuid`, delivery); err != nil {
		t.Fatal(err)
	}
	if status, replayed, raw := webhookHTTP(t, handler, http.MethodPost, replay, admin, map[string]any{}); status != http.StatusAccepted || replayed["data"].(map[string]any)["state"] != "pending" || replayed["data"].(map[string]any)["attempts"] != float64(0) {
		t.Fatalf("replay: %d %s", status, raw)
	}
	status, page, raw := webhookHTTP(t, handler, http.MethodGet, "/api/v1/webhooks/"+id+"/deliveries?state=pending&limit=1", admin, nil)
	if status != http.StatusOK || len(page["data"].(map[string]any)["deliveries"].([]any)) != 1 || page["data"].(map[string]any)["pagination"].(map[string]any)["nextCursor"] == "" {
		t.Fatalf("deliveries: %d %s", status, raw)
	}
	if status, _, raw := webhookHTTP(t, handler, http.MethodGet, "/api/v1/webhooks/"+id+"/deliveries/"+delivery, admin, nil); status != http.StatusOK || !strings.Contains(raw, `"history":[]`) {
		t.Fatalf("delivery: %d %s", status, raw)
	}
	if status, _, _ := webhookHTTP(t, handler, http.MethodDelete, "/api/v1/webhooks/"+id, admin, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	rows, err := store.Pool.Query(ctx, `SELECT event,after_state::text||before_state::text FROM audit_logs WHERE event LIKE 'webhook.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for rows.Next() {
		var event, state string
		if err = rows.Scan(&event, &state); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(state, secret) || strings.Contains(state, "media-header-value-Qz9") || strings.Contains(state, "team=media") {
			t.Fatalf("audit %s leaks %s", event, state)
		}
		events = append(events, event)
	}
	rows.Close()
	if got := strings.Join(events, ","); got != "webhook.created,webhook.updated,webhook.secret_rotated,webhook.delivery_replayed,webhook.deleted" {
		t.Fatalf("audit events %s", got)
	}
	_ = time.Second
}
