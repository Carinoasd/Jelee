package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/compat"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// httpClientControl builds the client control gate on a real store. The
// background writer is slow on purpose: the tests read hits through the
// administrator API, which flushes first.
func httpClientControl(t *testing.T, store *postgres.Store) *ClientControl {
	t.Helper()
	service, err := app.NewClientControl(store, ValidateClientRule)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClientControl(context.Background(), store, service, slog.New(slog.NewTextHandler(io.Discard, nil)), ClientControlOptions{FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

type ccResponse struct {
	status int
	code   string
	body   []byte
	header http.Header
}

func (r ccResponse) data(t *testing.T, target any) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(r.body, &envelope); err != nil || json.Unmarshal(envelope.Data, target) != nil {
		t.Fatalf("decode %d response %s", r.status, r.body)
	}
}

// ccRequest is one probe. compat sends the token in the compatibility
// layer's authorization header with the given client name and device ID.
type ccRequest struct {
	method, path, token string
	body                any
	userAgent           string
	compat              bool
	app, deviceID       string
	remote              string
	header              http.Header
}

func ccDo(t *testing.T, h http.Handler, q ccRequest) ccResponse {
	t.Helper()
	var body io.Reader
	if q.body != nil {
		raw, err := json.Marshal(q.body)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	} else if q.method != http.MethodGet && q.method != http.MethodHead && q.method != http.MethodDelete {
		body = strings.NewReader("{}")
	}
	r := httptest.NewRequest(q.method, "http://localhost"+q.path, body)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if q.remote != "" {
		r.RemoteAddr = q.remote
	}
	ua := q.userAgent
	if ua == "" {
		ua = "JeleeProbe/1.0"
	}
	r.Header.Set("User-Agent", ua)
	for k, v := range q.header {
		r.Header[k] = v
	}
	if q.compat {
		r.Header.Set("Authorization", compat.FormatClientAuth(compat.ClientAuth{Client: q.app, DeviceID: q.deviceID, Device: "probe", Version: "1.0", Token: q.token}))
	} else if q.token != "" {
		r.Header.Set("Authorization", "Bearer "+q.token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	return ccResponse{status: w.Code, code: envelope.Error.Code, body: w.Body.Bytes(), header: w.Header()}
}

// clientControlEnv is one PostgreSQL schema with the full router behind the
// client control gate, an administrator and helpers to add viewers and
// rules through the administrator API.
type clientControlEnv struct {
	ctx     context.Context
	store   *postgres.Store
	dsn     string
	handler http.Handler
	admin   string
	n       int
}

func newClientControlEnv(t *testing.T) *clientControlEnv {
	t.Helper()
	ctx, store, dsn := leakStore(t)
	e := &clientControlEnv{ctx: ctx, store: store, dsn: dsn}
	var err error
	if e.admin, err = store.Provision(ctx, "cc-admin", access.ClientNative, true); err != nil {
		t.Fatal(err)
	}
	e.handler = leakHandler(t, store, leakConfig(t, dsn, 0))
	return e
}

// viewer provisions a new native non-administrator session.
func (e *clientControlEnv) viewer(t *testing.T) string {
	t.Helper()
	e.n++
	token, err := e.store.Provision(e.ctx, "cc-viewer-"+strings.Repeat("v", e.n), access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (e *clientControlEnv) do(t *testing.T, q ccRequest) ccResponse {
	t.Helper()
	return ccDo(t, e.handler, q)
}

func (e *clientControlEnv) rule(t *testing.T, body map[string]any) domain.ClientRule {
	t.Helper()
	if _, ok := body["enabled"]; !ok {
		body["enabled"] = true
	}
	if _, ok := body["action"]; !ok {
		body["action"] = "deny"
	}
	r := e.do(t, ccRequest{method: http.MethodPost, path: "/api/v1/client-control/rules", token: e.admin, body: body})
	if r.status != http.StatusCreated {
		t.Fatalf("create rule %v: %d %s", body, r.status, r.body)
	}
	var rule domain.ClientRule
	r.data(t, &rule)
	return rule
}

func (e *clientControlEnv) deleteRule(t *testing.T, id string) {
	t.Helper()
	if r := e.do(t, ccRequest{method: http.MethodDelete, path: "/api/v1/client-control/rules/" + id, token: e.admin}); r.status != http.StatusNoContent {
		t.Fatalf("delete rule: %d %s", r.status, r.body)
	}
}

func (e *clientControlEnv) policy(t *testing.T, unknown string, admins, loopback bool) {
	t.Helper()
	r := e.do(t, ccRequest{method: http.MethodPut, path: "/api/v1/client-control/policy", token: e.admin, body: map[string]any{"unknownClients": unknown, "exemptAdmins": admins, "exemptLoopback": loopback}})
	if r.status != http.StatusOK {
		t.Fatalf("policy: %d %s", r.status, r.body)
	}
}

func (e *clientControlEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.store.Pool.QueryRow(e.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func me(token string) ccRequest {
	return ccRequest{method: http.MethodGet, path: "/api/v1/users/me", token: token}
}

func compatMe(token, app string) ccRequest {
	return ccRequest{method: http.MethodGet, path: "/compat/Users/Me", token: token, compat: true, app: app, deviceID: "dev-" + app}
}

func expect(t *testing.T, what string, r ccResponse, status int, code string) {
	t.Helper()
	if r.status != status || r.code != code {
		t.Fatalf("%s: got %d/%q, want %d/%q (%s)", what, r.status, r.code, status, code, r.body)
	}
}

// G47.10: every match kind and dimension family, through the native API and
// the compatibility layer, with a non-matching control each time.
func TestClientControlMatchKindsPostgres(t *testing.T) {
	e := newClientControlEnv(t)
	viewer := e.viewer(t)
	fingerprint := APIKeyFingerprint(viewer)
	cases := []struct {
		name      string
		rule      map[string]any
		hit, miss ccRequest
	}{
		{"exact user agent", map[string]any{"dimension": "user_agent", "match": "exact", "pattern": "BadAgent/1.0"},
			ccRequest{userAgent: "BadAgent/1.0"}, ccRequest{userAgent: "BadAgent/1.01"}},
		{"prefix case folded", map[string]any{"dimension": "user_agent", "match": "prefix", "pattern": "scraper/", "caseFold": true},
			ccRequest{userAgent: "SCRAPER/2 (linux)"}, ccRequest{userAgent: "my scraper/2"}},
		{"glob", map[string]any{"dimension": "user_agent", "match": "glob", "pattern": "*Bot?? (*)"},
			ccRequest{userAgent: "CrawlBot42 (x11)"}, ccRequest{userAgent: "CrawlBot4 (x11)"}},
		{"regex", map[string]any{"dimension": "user_agent", "match": "regex", "pattern": `^curl/[0-9]+\.`},
			ccRequest{userAgent: "curl/8.9.1"}, ccRequest{userAgent: "libcurl/8.9.1"}},
		{"cidr", map[string]any{"dimension": "ip", "match": "cidr", "pattern": "198.51.100.0/24"},
			ccRequest{remote: "198.51.100.77:4000"}, ccRequest{remote: "198.51.101.77:4000"}},
		{"exact ip", map[string]any{"dimension": "ip", "match": "exact", "pattern": "2001:db8::5"},
			ccRequest{remote: "[2001:db8::5]:4000"}, ccRequest{remote: "[2001:db8::6]:4000"}},
		{"header", map[string]any{"dimension": "header", "header": "X-Client-Trait", "match": "exact", "pattern": "evil"},
			ccRequest{header: http.Header{"X-Client-Trait": {"evil"}}}, ccRequest{header: http.Header{"X-Client-Trait": {"good"}}}},
		{"absent header", map[string]any{"dimension": "header", "header": "X-Required", "match": "absent", "pattern": ""},
			ccRequest{}, ccRequest{header: http.Header{"X-Required": {"1"}}}},
		{"api key fingerprint", map[string]any{"dimension": "api_key_fingerprint", "match": "exact", "pattern": fingerprint},
			ccRequest{}, ccRequest{token: e.viewer(t)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rule := e.rule(t, c.rule)
			defer e.deleteRule(t, rule.ID)
			hit, miss := me(viewer), me(viewer)
			hit.userAgent, hit.remote, hit.header = c.hit.userAgent, c.hit.remote, c.hit.header
			miss.userAgent, miss.remote, miss.header = c.miss.userAgent, c.miss.remote, c.miss.header
			if c.miss.token != "" {
				miss.token = c.miss.token
			}
			expect(t, "matching request", e.do(t, hit), http.StatusForbidden, "client_blocked")
			expect(t, "other request", e.do(t, miss), http.StatusOK, "")
			// The administrator is exempt by default (G47.7).
			admin := hit
			admin.token = e.admin
			expect(t, "administrator", e.do(t, admin), http.StatusOK, "")
		})
	}
	t.Run("compat app name and device id", func(t *testing.T) {
		native := e.viewer(t)
		for _, body := range []map[string]any{
			{"dimension": "app_name", "match": "exact", "pattern": "BadApp"},
			{"dimension": "device_id", "match": "exact", "pattern": "dev-BadApp"},
		} {
			rule := e.rule(t, body)
			blocked := e.do(t, compatMe(native, "BadApp"))
			if blocked.status != http.StatusForbidden || len(blocked.body) != 0 {
				t.Fatalf("%v: compat answered %d %q, want an empty 403", body, blocked.status, blocked.body)
			}
			if r := e.do(t, compatMe(native, "GoodApp")); r.status != http.StatusOK {
				t.Fatalf("%v: other compat client %d", body, r.status)
			}
			// The native API has no compat labels for a provisioned session.
			expect(t, "native API", e.do(t, me(native)), http.StatusOK, "")
			e.deleteRule(t, rule.ID)
		}
	})
}

// G47.3: every action, the observe to enforce switch and the hit records.
func TestClientControlActionsPostgres(t *testing.T) {
	e := newClientControlEnv(t)
	ua := func(q ccRequest, agent string) ccRequest { q.userAgent = agent; return q }

	t.Run("read_only", func(t *testing.T) {
		viewer := e.viewer(t)
		rule := e.rule(t, map[string]any{"dimension": "user_agent", "match": "exact", "pattern": "Reader/1", "action": "read_only"})
		defer e.deleteRule(t, rule.ID)
		expect(t, "read", e.do(t, ua(me(viewer), "Reader/1")), http.StatusOK, "")
		write := ccRequest{method: http.MethodPut, path: "/api/v1/users/me/profile", token: viewer, userAgent: "Reader/1", body: map[string]any{"displayName": "x", "locale": "en-US", "hidden": false}}
		expect(t, "write", e.do(t, write), http.StatusForbidden, "client_read_only")
		write.userAgent = "Writer/1"
		expect(t, "other client write", e.do(t, write), http.StatusOK, "")
		unplay := ccRequest{method: http.MethodDelete, path: "/compat/UserPlayedItems/00000000000000000000000000000001", token: viewer, compat: true, app: "Reader", userAgent: "Reader/1"}
		if r := e.do(t, unplay); r.status != http.StatusForbidden || len(r.body) != 0 {
			t.Fatalf("compat write by read-only client: %d %q", r.status, r.body)
		}
		info := ccRequest{method: http.MethodPost, path: "/compat/Items/00000000000000000000000000000001/PlaybackInfo", token: viewer, compat: true, app: "Reader", userAgent: "Reader/1"}
		if r := e.do(t, info); r.status == http.StatusForbidden {
			t.Fatal("read-shaped POST refused")
		}
	})

	t.Run("rate_limit", func(t *testing.T) {
		viewer := e.viewer(t)
		rule := e.rule(t, map[string]any{"dimension": "user_agent", "match": "prefix", "pattern": "Hammer/", "action": "rate_limit", "rateLimit": map[string]any{"requests": 2, "periodSeconds": 3600}})
		defer e.deleteRule(t, rule.ID)
		for i := 0; i < 2; i++ {
			expect(t, "within limit", e.do(t, ua(me(viewer), "Hammer/1")), http.StatusOK, "")
		}
		r := e.do(t, ua(me(viewer), "Hammer/1"))
		expect(t, "over limit", r, http.StatusTooManyRequests, "client_rate_limited")
		if n := r.header.Get("Retry-After"); n == "" || n == "0" {
			t.Fatalf("Retry-After %q", n)
		}
		// A compat client is its own client: it has its own budget.
		hammer := ccRequest{method: http.MethodGet, path: "/compat/Users/Me", token: viewer, compat: true, app: "Hammer", deviceID: "h", userAgent: "Hammer/1"}
		for i := 0; i < 2; i++ {
			if c := e.do(t, hammer); c.status != http.StatusOK {
				t.Fatalf("compat within limit: %d", c.status)
			}
		}
		if c := e.do(t, hammer); c.status != http.StatusTooManyRequests || c.header.Get("Retry-After") == "" {
			t.Fatalf("compat over limit: %d %v", c.status, c.header)
		}
		expect(t, "other client", e.do(t, ua(me(viewer), "Calm/1")), http.StatusOK, "")
	})

	t.Run("force_relogin", func(t *testing.T) {
		before := e.viewer(t)
		time.Sleep(10 * time.Millisecond)
		rule := e.rule(t, map[string]any{"dimension": "user_agent", "match": "exact", "pattern": "Stale/1", "action": "force_relogin"})
		defer e.deleteRule(t, rule.ID)
		after := e.viewer(t)
		expect(t, "session issued before the rule", e.do(t, ua(me(before), "Stale/1")), http.StatusUnauthorized, "authentication_required")
		expect(t, "revoked session with another client", e.do(t, ua(me(before), "Fresh/1")), http.StatusUnauthorized, "authentication_required")
		expect(t, "session issued after the rule", e.do(t, ua(me(after), "Stale/1")), http.StatusOK, "")
		if e.count(t, `SELECT count(*) FROM audit_logs WHERE event='client_control.session_revoked' AND category='security'`) != 1 {
			t.Fatal("forced relogin not in the security log")
		}
	})

	t.Run("observe then enforce", func(t *testing.T) {
		viewer := e.viewer(t)
		rule := e.rule(t, map[string]any{"dimension": "user_agent", "match": "exact", "pattern": "Trial/1", "action": "observe", "intent": "deny"})
		defer e.deleteRule(t, rule.ID)
		for i := 0; i < 3; i++ {
			expect(t, "observed", e.do(t, ua(me(viewer), "Trial/1")), http.StatusOK, "")
		}
		var stats domain.ClientHitStats
		e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/stats", token: e.admin}).data(t, &stats)
		if stats.Observed < 3 || stats.Blocked != 0 {
			t.Fatalf("observe impact not visible: %+v", stats)
		}
		r := e.do(t, ccRequest{method: http.MethodPost, path: "/api/v1/client-control/rules/" + rule.ID + "/enforce", token: e.admin})
		var enforced domain.ClientRule
		r.data(t, &enforced)
		if enforced.Action != "deny" || enforced.Intent != "" || enforced.HitCount < 3 {
			t.Fatalf("enforce switch: %+v", enforced)
		}
		expect(t, "enforced", e.do(t, ua(me(viewer), "Trial/1")), http.StatusForbidden, "client_blocked")
		e.do(t, ccRequest{method: http.MethodPost, path: "/api/v1/client-control/rules/" + rule.ID + "/observe", token: e.admin}).data(t, &enforced)
		if enforced.Action != "observe" || enforced.Intent != "deny" {
			t.Fatalf("observe switch: %+v", enforced)
		}
		expect(t, "observed again", e.do(t, ua(me(viewer), "Trial/1")), http.StatusOK, "")
		if e.count(t, `SELECT count(*) FROM audit_logs WHERE event='client_control.rule_mode_changed'`) != 2 {
			t.Fatal("mode switches not audited")
		}
	})

	t.Run("shadow stays out of statistics", func(t *testing.T) {
		viewer := e.viewer(t)
		rule := e.rule(t, map[string]any{"dimension": "user_agent", "match": "exact", "pattern": "Shade/1", "action": "shadow", "intent": "deny"})
		defer e.deleteRule(t, rule.ID)
		expect(t, "shadowed", e.do(t, ua(me(viewer), "Shade/1")), http.StatusOK, "")
		var page struct {
			Hits []domain.ClientHitRecord `json:"hits"`
		}
		e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/hits?mode=shadow&ruleId=" + rule.ID, token: e.admin}).data(t, &page)
		if len(page.Hits) != 1 || page.Hits[0].Action != "deny" || page.Hits[0].UserAgent != "Shade/1" {
			t.Fatalf("shadow hit: %+v", page.Hits)
		}
		var stats domain.ClientHitStats
		e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/stats?top=50", token: e.admin}).data(t, &stats)
		for _, c := range stats.TopUserAgents {
			if c.Value == "Shade/1" {
				t.Fatal("shadow hit counted in statistics")
			}
		}
	})

	t.Run("allow list and unknown clients", func(t *testing.T) {
		viewer := e.viewer(t)
		e.policy(t, "pending_approval", true, true)
		defer e.policy(t, "allow", true, true)
		expect(t, "unknown client", e.do(t, ua(me(viewer), "NewApp/1")), http.StatusForbidden, "client_pending_approval")
		// The client appears for approval; trusting it lets it in.
		var page struct {
			Clients []domain.KnownClient `json:"clients"`
		}
		e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/clients", token: e.admin}).data(t, &page)
		id := ""
		for _, c := range page.Clients {
			if c.UserAgent == "NewApp/1" {
				id = c.ID
			}
		}
		if id == "" {
			t.Fatalf("pending client not listed: %+v", page.Clients)
		}
		r := e.do(t, ccRequest{method: http.MethodPatch, path: "/api/v1/client-control/clients/" + id, token: e.admin, body: map[string]any{"trusted": true, "alias": "Approved app"}})
		var updated domain.KnownClient
		r.data(t, &updated)
		if !updated.Trusted || updated.Alias != "Approved app" {
			t.Fatalf("trust: %+v", updated)
		}
		expect(t, "trusted client", e.do(t, ua(me(viewer), "NewApp/1")), http.StatusOK, "")
		e.policy(t, "deny", true, true)
		expect(t, "denied unknown", e.do(t, ua(me(viewer), "Other/1")), http.StatusForbidden, "client_blocked")
		allow := e.rule(t, map[string]any{"dimension": "user_agent", "match": "prefix", "pattern": "Other/", "action": "allow"})
		defer e.deleteRule(t, allow.ID)
		expect(t, "allow-listed", e.do(t, ua(me(viewer), "Other/1")), http.StatusOK, "")
		e.policy(t, "read_only", true, true)
		write := ccRequest{method: http.MethodPut, path: "/api/v1/users/me/profile", token: viewer, userAgent: "Unlisted/1", body: map[string]any{"displayName": "x", "locale": "en-US", "hidden": false}}
		expect(t, "read-only unknown", e.do(t, write), http.StatusForbidden, "client_read_only")
	})

	t.Run("loopback", func(t *testing.T) {
		viewer := e.viewer(t)
		rule := e.rule(t, map[string]any{"dimension": "user_agent", "match": "exact", "pattern": "Local/1"})
		defer e.deleteRule(t, rule.ID)
		local := ua(me(viewer), "Local/1")
		local.remote = "127.0.0.1:5000"
		expect(t, "loopback exempt", e.do(t, local), http.StatusOK, "")
		local.header = http.Header{"X-Forwarded-For": {"203.0.113.5"}}
		expect(t, "proxied loopback", e.do(t, local), http.StatusForbidden, "client_blocked")
		e.policy(t, "allow", true, false)
		defer e.policy(t, "allow", true, true)
		local.header = nil
		expect(t, "loopback exemption off", e.do(t, local), http.StatusForbidden, "client_blocked")
	})

	t.Run("logins", func(t *testing.T) {
		rule := e.rule(t, map[string]any{"dimension": "user_agent", "match": "exact", "pattern": "Blocked/1"})
		defer e.deleteRule(t, rule.ID)
		login := ccRequest{method: http.MethodPost, path: "/api/v1/auth/login", userAgent: "Blocked/1", body: map[string]any{"name": "nobody", "password": "irrelevant"}}
		expect(t, "web login", e.do(t, login), http.StatusForbidden, "client_blocked")
		login.path = "/api/v1/auth/login/native"
		login.body = map[string]any{"name": "nobody", "password": "irrelevant", "client": "C", "device": "D", "deviceId": "I", "version": "1"}
		expect(t, "native login", e.do(t, login), http.StatusForbidden, "client_blocked")
		compatLogin := ccRequest{method: http.MethodPost, path: "/compat/Users/AuthenticateByName", compat: true, app: "C", deviceID: "I", userAgent: "Blocked/1", body: map[string]any{"Username": "nobody", "Pw": "irrelevant"}}
		if r := e.do(t, compatLogin); r.status != http.StatusForbidden {
			t.Fatalf("compat login: %d", r.status)
		}
		login.userAgent = "Allowed/1"
		expect(t, "login of another client reaches the password check", e.do(t, login), http.StatusUnauthorized, "authentication_required")
	})

	t.Run("hits, export, statistics", func(t *testing.T) {
		viewer := e.viewer(t)
		rule := e.rule(t, map[string]any{"dimension": "user_agent", "match": "exact", "pattern": "Logged/1"})
		defer e.deleteRule(t, rule.ID)
		q := ua(me(viewer), "Logged/1")
		q.remote = "203.0.113.77:1234"
		for i := 0; i < 4; i++ {
			e.do(t, q)
		}
		var page struct {
			Hits []domain.ClientHitRecord `json:"hits"`
		}
		e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/hits?ruleId=" + rule.ID, token: e.admin}).data(t, &page)
		// Four requests aggregate into one bucket per minute.
		var total int64
		for _, h := range page.Hits {
			total += h.Hits
			if h.Network != "203.0.113.0/24" || h.Mode != "enforced" || h.Surface != "native" || h.Action != "deny" || h.UserAgent != "Logged/1" {
				t.Fatalf("masked hit: %+v", h)
			}
		}
		if total != 4 || len(page.Hits) > 2 {
			t.Fatalf("aggregated hits: %+v", page.Hits)
		}
		if bytes.Contains(e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/hits", token: e.admin}).body, []byte("203.0.113.77")) {
			t.Fatal("hit listing leaks the full address")
		}
		export := e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/hits/export?ruleId=" + rule.ID, token: e.admin})
		if export.status != http.StatusOK || !strings.Contains(export.header.Get("Content-Disposition"), "attachment") || bytes.Contains(export.body, []byte("203.0.113.77")) || bytes.Contains(export.body, []byte("/api/v1")) {
			t.Fatalf("export: %d %s", export.status, export.body)
		}
		if e.count(t, `SELECT count(*) FROM audit_logs WHERE event='client_control.hits_exported'`) != 1 {
			t.Fatal("export not audited")
		}
		var stats domain.ClientHitStats
		e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/stats?hours=1&top=5", token: e.admin}).data(t, &stats)
		top := func(list []domain.ClientHitCount, value string) int64 {
			for _, c := range list {
				if c.Value == value {
					return c.Hits
				}
			}
			return 0
		}
		if top(stats.TopUserAgents, "Logged/1") != 4 || top(stats.TopIPs, "203.0.113.77") < 4 || top(stats.TopRules, rule.ID) != 4 || stats.Blocked < 4 {
			t.Fatalf("statistics: %+v", stats)
		}
		var stored domain.ClientRule
		e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/rules/" + rule.ID, token: e.admin}).data(t, &stored)
		if stored.HitCount != 4 || stored.LastHitAt == nil {
			t.Fatalf("hit counter: %+v", stored)
		}
	})

	t.Run("known clients block and kick", func(t *testing.T) {
		viewer := e.viewer(t)
		other := e.viewer(t)
		q := compatMe(viewer, "Kickable")
		if r := e.do(t, q); r.status != http.StatusOK {
			t.Fatalf("compat request %d", r.status)
		}
		var page struct {
			Clients []domain.KnownClient `json:"clients"`
		}
		e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/clients", token: e.admin}).data(t, &page)
		var client domain.KnownClient
		for _, c := range page.Clients {
			if c.AppName == "Kickable" {
				client = c
			}
		}
		if client.ID == "" || client.DeviceID != "dev-Kickable" || client.ActiveSessions != 1 || client.LastIP == "" || client.ClientKind != "native" {
			t.Fatalf("known client: %+v", page.Clients)
		}
		var kicked map[string]int64
		e.do(t, ccRequest{method: http.MethodPost, path: "/api/v1/client-control/clients/" + client.ID + "/kick", token: e.admin}).data(t, &kicked)
		if kicked["sessionsRevoked"] != 1 {
			t.Fatalf("kick: %v", kicked)
		}
		expect(t, "kicked session", e.do(t, me(viewer)), http.StatusUnauthorized, "authentication_required")
		expect(t, "other user", e.do(t, me(other)), http.StatusOK, "")
		r := e.do(t, ccRequest{method: http.MethodPost, path: "/api/v1/client-control/clients/" + client.ID + "/block", token: e.admin})
		var rule domain.ClientRule
		r.data(t, &rule)
		defer e.deleteRule(t, rule.ID)
		if r.status != http.StatusCreated || rule.Dimension != "device_id" || rule.Pattern != "dev-Kickable" || rule.Action != "deny" {
			t.Fatalf("block: %d %+v", r.status, rule)
		}
		if r := e.do(t, compatMe(other, "Kickable")); r.status != http.StatusForbidden {
			t.Fatalf("blocked device answered %d", r.status)
		}
		for _, event := range []string{"client_control.client_kicked", "client_control.client_blocked"} {
			if e.count(t, `SELECT count(*) FROM audit_logs WHERE event=$1 AND target_id=$2::uuid`, event, client.ID) != 1 {
				t.Fatalf("%s not audited", event)
			}
		}
	})
}

// G47.7: a rule set that locks out everyone, administrators included, is
// undone by the emergency reset, and the change reaches a second server
// instance on its next request through the version check.
func TestClientControlLockoutRecoveryAndInstancesPostgres(t *testing.T) {
	e := newClientControlEnv(t)
	viewer := e.viewer(t)
	second := leakHandler(t, e.store, leakConfig(t, e.dsn, 0))
	expect(t, "second instance before", ccDo(t, second, me(viewer)), http.StatusOK, "")
	e.rule(t, map[string]any{"dimension": "user_agent", "match": "glob", "pattern": "*", "priority": 10})
	expect(t, "second instance sees the new rule on the next request", ccDo(t, second, me(viewer)), http.StatusForbidden, "client_blocked")
	expect(t, "exempt administrator", e.do(t, me(e.admin)), http.StatusOK, "")
	e.policy(t, "deny", false, false)
	expect(t, "administrator locked out", e.do(t, me(e.admin)), http.StatusForbidden, "client_blocked")
	expect(t, "administrator API locked out", e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/rules", token: e.admin}), http.StatusForbidden, "client_blocked")

	result, err := e.store.ResetClientPolicies(e.ctx)
	if err != nil || result.RulesDisabled != 1 || result.Policy.UnknownClients != "allow" || !result.Policy.ExemptAdmins || !result.Policy.ExemptLoopback {
		t.Fatalf("reset: %+v %v", result, err)
	}
	expect(t, "administrator after reset", e.do(t, me(e.admin)), http.StatusOK, "")
	expect(t, "viewer after reset", e.do(t, me(viewer)), http.StatusOK, "")
	expect(t, "second instance after reset", ccDo(t, second, me(viewer)), http.StatusOK, "")
	if e.count(t, `SELECT count(*) FROM audit_logs WHERE event='client_control.policies_reset' AND category='security' AND actor_id IS NULL`) != 1 {
		t.Fatal("reset not in the security log")
	}
}

// Administrator API validation, roles and audit (G47.2, G47.5).
func TestClientControlAdministrationPostgres(t *testing.T) {
	e := newClientControlEnv(t)
	viewer := e.viewer(t)
	expect(t, "viewer", e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/rules", token: viewer}), http.StatusForbidden, "forbidden")
	invalid := []map[string]any{
		{"dimension": "user_agent", "match": "regex", "pattern": "(unclosed", "action": "deny", "enabled": true},
		{"dimension": "user_agent", "match": "regex", "pattern": `(a{1,1000}){1,1000}`, "action": "deny", "enabled": true},
		{"dimension": "user_agent", "match": "exact", "pattern": "x", "action": "restrict_libraries", "enabled": true},
		{"dimension": "user_agent", "match": "exact", "pattern": "x", "action": "deny", "scopeKind": "library", "scopeValues": []string{"a"}, "enabled": true},
		{"dimension": "user_agent", "match": "exact", "pattern": "x", "action": "deny", "scopeKind": "user", "scopeValues": []string{"not-a-uuid"}, "enabled": true},
		{"dimension": "user_agent", "match": "cidr", "pattern": "10.0.0.0/8", "action": "deny", "enabled": true},
		{"dimension": "user_agent", "match": "exact", "pattern": "x", "action": "observe", "enabled": true},
		{"dimension": "user_agent", "match": "exact", "pattern": "x", "action": "rate_limit", "enabled": true},
		{"dimension": "user_agent", "match": "exact", "pattern": "x", "action": "deny", "window": map[string]any{"dailyStart": "25:00", "dailyEnd": "01:00"}, "enabled": true},
		{"dimension": "user_agent", "match": "exact", "pattern": "x", "action": "deny"},
	}
	for _, body := range invalid {
		expect(t, "invalid rule", e.do(t, ccRequest{method: http.MethodPost, path: "/api/v1/client-control/rules", token: e.admin, body: body}), http.StatusBadRequest, "invalid_request")
	}
	from := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	rule := e.rule(t, map[string]any{"dimension": "user_agent", "match": "exact", "pattern": "Windowed/1", "priority": 5, "note": "night\tonly",
		"scopeKind": "client_kind", "scopeValues": []string{"native"}, "window": map[string]any{"from": from, "dailyStart": "00:00", "dailyEnd": "24:00", "weekdays": []int{0, 1, 2, 3, 4, 5, 6}, "timeZone": "Asia/Taipei"}})
	if rule.Window == nil || !rule.Window.From.Equal(from) || rule.Window.TimeZone != "Asia/Taipei" || len(rule.Window.Weekdays) != 7 || rule.ScopeKind != "client_kind" {
		t.Fatalf("stored rule: %+v", rule)
	}
	expect(t, "windowed scoped rule", e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/users/me", token: viewer, userAgent: "Windowed/1"}), http.StatusForbidden, "client_blocked")
	// A rule scoped to another user does not apply.
	var userID string
	if err := e.store.Pool.QueryRow(e.ctx, `SELECT id::text FROM users WHERE name='cc-admin'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	update := map[string]any{"dimension": "user_agent", "match": "exact", "pattern": "Windowed/1", "action": "deny", "scopeKind": "user", "scopeValues": []string{userID}, "enabled": true}
	r := e.do(t, ccRequest{method: http.MethodPut, path: "/api/v1/client-control/rules/" + rule.ID, token: e.admin, body: update})
	if r.status != http.StatusOK {
		t.Fatalf("update %d %s", r.status, r.body)
	}
	expect(t, "rule scoped to another user", e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/users/me", token: viewer, userAgent: "Windowed/1"}), http.StatusOK, "")
	// Unchanged updates write no audit row.
	e.do(t, ccRequest{method: http.MethodPut, path: "/api/v1/client-control/rules/" + rule.ID, token: e.admin, body: update})
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE event='client_control.rule_updated' AND target_id=$1::uuid`, rule.ID); n != 1 {
		t.Fatalf("rule updates audited %d times", n)
	}
	e.policy(t, "allow", true, true)
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE event='client_control.policy_changed'`); n != 0 {
		t.Fatalf("unchanged policy audited %d times", n)
	}
	e.policy(t, "read_only", true, true)
	var policy domain.ClientPolicy
	e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/policy", token: e.admin}).data(t, &policy)
	if policy.UnknownClients != "read_only" || policy.Version < 4 {
		t.Fatalf("policy: %+v", policy)
	}
	e.deleteRule(t, rule.ID)
	expect(t, "deleted rule", e.do(t, ccRequest{method: http.MethodGet, path: "/api/v1/client-control/rules/" + rule.ID, token: e.admin}), http.StatusNotFound, "not_found")
	for _, event := range []string{"client_control.rule_created", "client_control.rule_deleted", "client_control.policy_changed"} {
		if e.count(t, `SELECT count(*) FROM audit_logs WHERE event=$1`, event) != 1 {
			t.Fatalf("%s not audited once", event)
		}
	}
}

// C7: the request path with 10000 mixed rules against the same path with
// none, on PostgreSQL-backed sessions. The ratio is logged for the owner's
// verification record; the assertion only catches a gross regression,
// since shared test machines make tight timing assertions flaky.
func TestClientControlOverheadPostgres(t *testing.T) {
	e := newClientControlEnv(t)
	viewer := e.viewer(t)
	q := me(viewer)
	q.userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36"
	measure := func() time.Duration {
		for range 50 {
			e.do(t, q) // warm the pool, the plan cache and the gate
		}
		samples := make([]time.Duration, 0, 7)
		for range 7 {
			start := time.Now()
			for range 200 {
				if r := e.do(t, q); r.status != http.StatusOK {
					t.Fatalf("probe %d", r.status)
				}
			}
			samples = append(samples, time.Since(start)/200)
		}
		slices.Sort(samples)
		return samples[len(samples)/2]
	}
	base := measure()
	if _, err := e.store.Pool.Exec(e.ctx, `INSERT INTO client_rules(dimension,match_kind,pattern,case_fold,priority,action,intent)
 SELECT d,m,p,f,n%7,a,CASE WHEN a='observe' THEN 'deny' END FROM (
  SELECT n,
   CASE WHEN n%10<4 THEN 'device_id' WHEN n%10 IN (4,5) THEN 'app_name' WHEN n%10 IN (6,7) THEN 'user_agent' WHEN n%10=8 AND n%100=8 THEN 'user_agent' WHEN n%10=8 THEN 'device_name' ELSE 'ip' END AS d,
   CASE WHEN n%10<6 THEN 'exact' WHEN n%10=6 THEN 'prefix' WHEN n%10=7 THEN 'glob' WHEN n%10=8 AND n%100=8 THEN 'regex' WHEN n%10=8 THEN 'exact' ELSE 'cidr' END AS m,
   CASE WHEN n%10<4 THEN 'device-'||n WHEN n%10=4 THEN 'Observed'||n WHEN n%10=5 THEN 'app'||n WHEN n%10=6 THEN 'client'||n||'/' WHEN n%10=7 THEN '*Bot'||n||'*(*)'
    WHEN n%10=8 AND n%100=8 THEN '(?:crawler|spider)-'||n||'\b' WHEN n%10=8 THEN 'Room'||n ELSE '10.'||((n/256)%256)||'.'||(n%256)||'.0/24' END AS p,
   n%10 IN (5,6) AS f,
   CASE WHEN n%10=4 THEN 'observe' ELSE 'deny' END AS a
  FROM generate_series(0,9999) n) r`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Pool.Exec(e.ctx, `UPDATE client_control_policy SET version=version+1`); err != nil {
		t.Fatal(err)
	}
	ruled := measure()
	ratio := float64(ruled) / float64(base)
	t.Logf("C7 client control overhead: no rules %v/request, 10000 rules %v/request, ratio %.3f", base, ruled, ratio)
	if ratio > 1.5 {
		t.Fatalf("10000 rules cost %.0f%% over no rules", (ratio-1)*100)
	}
}
