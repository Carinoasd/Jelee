package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// TestContentAccessHTTPPostgres drives the content access administration
// API (G48.1, G48.4) against PostgreSQL: viewers are refused, bodies are
// validated, every change is audited, and a rule applies to the viewer's
// next request through the unified filter.
func TestContentAccessHTTPPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	f := leakFixture(t, ctx, store)
	handler := leakHandler(t, store, leakConfig(t, dsn, 0))
	// The viewer may see both libraries; rules alone hide content below.
	if _, err := store.Pool.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, f.viewer, f.library[leakHidden]); err != nil {
		t.Fatal(err)
	}
	item := f.item[leakHidden]
	base := "/api/v1/users/" + f.viewer + "/content-access"
	ruleURL := base + "/items/" + item
	listed := func() bool {
		t.Helper()
		status, _, body := webhookHTTP(t, handler, http.MethodGet, "/api/v1/items?limit=100", f.viewerToken, nil)
		if status != http.StatusOK {
			t.Fatalf("viewer listing %d %s", status, body)
		}
		return strings.Contains(body, item)
	}
	if !listed() {
		t.Fatal("granted item not listed before any rule")
	}

	// Viewers are refused before any lookup.
	for _, probe := range []struct{ method, path string }{{http.MethodGet, base}, {http.MethodPut, base}, {http.MethodPut, ruleURL}, {http.MethodDelete, ruleURL},
		{http.MethodGet, "/api/v1/access/policy"}, {http.MethodPut, "/api/v1/access/policy"}, {http.MethodGet, "/api/v1/access/parental-ratings"}} {
		if status, _, _ := webhookHTTP(t, handler, probe.method, probe.path, f.viewerToken, map[string]any{}); status != http.StatusForbidden {
			t.Fatalf("viewer %s %s: %d", probe.method, probe.path, status)
		}
	}

	// Validation.
	for _, bad := range []any{
		map[string]any{"parentalRatingMax": 13},
		map[string]any{"parentalRatingMax": 22, "blockedTags": []string{}},
		map[string]any{"blockedTags": []string{" "}},
		map[string]any{"blockedTags": []string{}, "unknown": true},
		map[string]any{"blockedTags": []string{}, "blockUnrated": nil},
	} {
		if status, _, body := webhookHTTP(t, handler, http.MethodPut, base, f.adminToken, bad); status != http.StatusBadRequest {
			t.Fatalf("invalid %v: %d %s", bad, status, body)
		}
	}
	if status, _, _ := webhookHTTP(t, handler, http.MethodPut, ruleURL, f.adminToken, map[string]any{"effect": "deny"}); status != http.StatusBadRequest {
		t.Fatalf("invalid effect: %d", status)
	}
	if status, _, _ := webhookHTTP(t, handler, http.MethodPut, base+"/items/00000000-0000-4000-8000-000000000001", f.adminToken, map[string]any{"effect": "hide"}); status != http.StatusNotFound {
		t.Fatalf("missing item: %d", status)
	}
	if status, _, _ := webhookHTTP(t, handler, http.MethodPut, "/api/v1/access/policy", f.adminToken, map[string]any{"restrictAdmins": true}); status != http.StatusBadRequest {
		t.Fatalf("partial policy: %d", status)
	}

	// A hide rule takes effect on the next request and is audited.
	status, envelope, body := webhookHTTP(t, handler, http.MethodPut, ruleURL, f.adminToken, map[string]any{"effect": "hide"})
	if status != http.StatusOK || envelope["data"].(map[string]any)["effect"] != "hide" {
		t.Fatalf("set rule %d %s", status, body)
	}
	if listed() {
		t.Fatal("hidden item still listed")
	}
	if status, _, _ := webhookHTTP(t, handler, http.MethodGet, "/api/v1/items/"+item, f.viewerToken, nil); status != http.StatusNotFound {
		t.Fatalf("hidden item by ID: %d", status)
	}
	status, envelope, body = webhookHTTP(t, handler, http.MethodGet, base, f.adminToken, nil)
	data, _ := envelope["data"].(map[string]any)
	if status != http.StatusOK || data == nil || len(data["rules"].([]any)) != 1 || data["blockedTags"] == nil || data["parentalRatingMax"] != nil || data["blockUnrated"] != nil {
		t.Fatalf("read content access %d %s", status, body)
	}
	if status, _, body = webhookHTTP(t, handler, http.MethodDelete, ruleURL, f.adminToken, nil); status != http.StatusNoContent {
		t.Fatalf("delete rule %d %s", status, body)
	}
	if status, _, _ = webhookHTTP(t, handler, http.MethodDelete, ruleURL, f.adminToken, nil); status != http.StatusNotFound {
		t.Fatalf("delete missing rule %d", status)
	}
	if !listed() {
		t.Fatal("item not listed after its rule was removed")
	}

	// A ceiling hides the rated item; the policy and rating table are readable.
	if _, err := store.Pool.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,2)`, item); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO item_metadata_fields(item_id,field,value,source,updated_at) VALUES($1::uuid,'mpaa','TV-MA','manual',now())`, item); err != nil {
		t.Fatal(err)
	}
	status, _, body = webhookHTTP(t, handler, http.MethodPut, base, f.adminToken, map[string]any{"parentalRatingMax": 14, "blockedTags": []string{"Horror"}})
	if status != http.StatusOK || !strings.Contains(body, `"blockedTags":["horror"]`) || !strings.Contains(body, `"parentalRatingMax":14`) {
		t.Fatalf("set content access %d %s", status, body)
	}
	if listed() {
		t.Fatal("item above the ceiling still listed")
	}
	if status, _, body = webhookHTTP(t, handler, http.MethodPut, "/api/v1/access/policy", f.adminToken, map[string]any{"restrictAdmins": false, "blockUnrated": true}); status != http.StatusOK {
		t.Fatalf("set policy %d %s", status, body)
	}
	if status, _, body = webhookHTTP(t, handler, http.MethodGet, "/api/v1/access/policy", f.adminToken, nil); status != http.StatusOK || !strings.Contains(body, `"blockUnrated":true`) {
		t.Fatalf("read policy %d %s", status, body)
	}
	if status, _, body = webhookHTTP(t, handler, http.MethodGet, "/api/v1/access/parental-ratings", f.adminToken, nil); status != http.StatusOK || !strings.Contains(body, `{"code":"TV-MA","level":17}`) {
		t.Fatalf("parental ratings %d %s", status, body)
	}
	var audited int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE event IN ('user.item_access_rule_set','user.item_access_rule_removed','user.content_access_changed','access.policy_changed')`).Scan(&audited); err != nil || audited != 4 {
		t.Fatalf("audit rows %d %v", audited, err)
	}
}
