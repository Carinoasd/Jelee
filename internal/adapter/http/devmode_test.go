package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/go-chi/chi/v5"
)

type devTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *devTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *devTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// devBackend authenticates "a" tokens as an administrator and "v" tokens
// as a viewer.
func devBackend() *fakeBackend {
	return &fakeBackend{auth: func(_ context.Context, token string) (access.Principal, error) {
		switch token {
		case strings.Repeat("a", 43):
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative, Admin: true}, nil
		case strings.Repeat("v", 43):
			return access.Principal{UserID: "00000000-0000-4000-8000-0000000000aa", SessionID: sessionID, Kind: access.ClientNative}, nil
		}
		return access.Principal{}, domain.ErrUnauthenticated
	}}
}

func devConfig(env, enabled bool, environment string) config.Config {
	cfg := ReferenceConfig()
	cfg.Dev = config.DevConfig{EnvFlag: env, Enabled: enabled, Environment: environment}
	return cfg
}

type devFixture struct {
	handler http.Handler
	ctrl    *devmode.Controller
	store   *devmode.MemoryStore
	clock   *devTestClock
	logs    *bytes.Buffer
}

func newDevFixture(t *testing.T, cfg config.Config) devFixture {
	t.Helper()
	f := devFixture{store: devmode.NewMemoryStore(), clock: &devTestClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}, logs: &bytes.Buffer{}}
	var err error
	f.ctrl, err = devmode.NewController(devmode.ControllerOptions{Store: f.store, Clock: f.clock, Local: cfg.Dev.Inputs(), TTL: time.Hour,
		Available: []devmode.Toggle{devmode.RelaxHostStrict, devmode.RelaxLoginRateLimit, devmode.DebugBodyLogging, devmode.DebugPprof}})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(f.logs, nil))
	f.handler = contractRouterWith(t, cfg, devBackend(), logger, WithDevMode(f.ctrl))
	return f
}

// enable opens a session the way jelee-cli does, with a token from the
// loopback entry.
func (f devFixture) enable(t *testing.T) {
	t.Helper()
	w := devRequest(f.handler, "POST", DevAPIPrefix+"/token", "{}", "", "127.0.0.1:40000")
	if w.Code != http.StatusCreated {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}
	var issued struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctrl.Enable(context.Background(), devmode.Actor{}, issued.Data.Token, "cli", "", 0); err != nil {
		t.Fatal(err)
	}
}

func devRequest(h http.Handler, method, path, body, token, remote string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+strings.Repeat(token, 43))
	}
	if remote != "" {
		r.RemoteAddr = remote
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			r.Host = headers[i+1]
			continue
		}
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func devRoutesOf(t *testing.T, h http.Handler) []string {
	t.Helper()
	var routes []string
	if err := chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, DevAPIPrefix) || strings.HasPrefix(route, "/debug/") {
			routes = append(routes, method+" "+route)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(routes)
	return routes
}

func devConcretePath(route string) string {
	return strings.NewReplacer("{toggle}", string(devmode.RelaxHostStrict), "*", "heap").Replace(route)
}

// TestDevModeUnreachableInProduction is G45.8: every developer route a
// capable instance registers is absent (404) from production and from every
// configuration missing a threshold, every registered route answers with
// X-Jelee-Dev-Mode: false, and the specification names no developer path,
// even when shared storage holds an active session with every toggle on.
func TestDevModeUnreachableInProduction(t *testing.T) {
	capable := newDevFixture(t, devConfig(true, true, "development"))
	devRoutes := devRoutesOf(t, capable.handler)
	if len(devRoutes) != 6 {
		t.Fatalf("capable instance registered %v", devRoutes)
	}
	capable.enable(t)
	for _, toggle := range []devmode.Toggle{devmode.RelaxHostStrict, devmode.DebugPprof} {
		if _, err := capable.ctrl.SetToggle(context.Background(), devmode.Actor{}, toggle, true, devmode.Confirmation{IUnderstand: true}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		cfg  config.Config
	}{
		{"default", ReferenceConfig()},
		{"production with every switch", devConfig(true, true, "production")},
		{"production label case", devConfig(true, true, " Production ")},
		{"environment flag only", devConfig(true, false, "")},
		{"configuration only", devConfig(false, true, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A controller that applies the active session is passed anyway;
			// the instance configuration alone decides.
			ctrl, err := devmode.NewController(devmode.ControllerOptions{Store: capable.store, Clock: capable.clock, Local: devmode.Inputs{EnvFlag: true, ConfigEnabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			if err = ctrl.Refresh(context.Background()); err != nil || !ctrl.Effective(devmode.RelaxHostStrict) {
				t.Fatal("shared session not loaded", err)
			}
			h := contractRouterWith(t, tc.cfg, devBackend(), slog.New(slog.DiscardHandler), WithDevMode(ctrl))
			if got := devRoutesOf(t, h); len(got) != 0 {
				t.Fatalf("developer routes registered: %v", got)
			}
			for _, route := range devRoutes {
				method, path, _ := strings.Cut(route, " ")
				for _, token := range []string{"", "a"} {
					w := devRequest(h, method, devConcretePath(path), `{"enabled":true,"iUnderstand":true}`, token, "127.0.0.1:40000")
					if w.Code != http.StatusNotFound || w.Header().Get("X-Jelee-Dev-Mode") != "false" {
						t.Fatalf("%s reachable: %d %s", route, w.Code, w.Body)
					}
				}
			}
			// Every registered route, walked automatically.
			if err := chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
				w := devRequest(h, method, devConcretePath(strings.ReplaceAll(route, "{", "x")), "", "a", "127.0.0.1:40000", "Host", "elsewhere.example")
				if w.Header().Get("X-Jelee-Dev-Mode") != "false" || w.Code != http.StatusBadRequest {
					t.Errorf("%s %s: %d dev=%q (Host relaxation must not apply)", method, route, w.Code, w.Header().Get("X-Jelee-Dev-Mode"))
				}
				w = devRequest(h, method, devConcretePath(strings.ReplaceAll(route, "{", "x")), "", "", "")
				if w.Header().Get("X-Jelee-Dev-Mode") != "false" {
					t.Errorf("%s %s: dev header %q", method, route, w.Header().Get("X-Jelee-Dev-Mode"))
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var system struct {
				Data map[string]any `json:"data"`
			}
			w := devRequest(h, "GET", "/api/v1/system", "", "", "")
			if json.Unmarshal(w.Body.Bytes(), &system) != nil || system.Data["devMode"] != false || system.Data["devModeExpiresAt"] != nil {
				t.Fatalf("system info: %s", w.Body)
			}
			for path := range Specification(tc.cfg)["paths"].(map[string]any) {
				if strings.HasPrefix(path, DevAPIPrefix) || strings.HasPrefix(path, "/debug/") {
					t.Fatalf("specification documents %s", path)
				}
			}
		})
	}
}

func TestDevModeCapableInstanceNeedsAccountsAndController(t *testing.T) {
	cfg := devConfig(true, true, "")
	if _, err := contractServer(t, cfg, devBackend(), slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("capable configuration without its controller: %v", err)
	}
	incapable, err := devmode.NewController(devmode.ControllerOptions{Store: devmode.NewMemoryStore(), Local: devmode.Inputs{EnvFlag: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = contractServer(t, cfg, devBackend(), slog.New(slog.DiscardHandler), WithDevMode(incapable)); err == nil {
		t.Fatal("a controller with other thresholds was accepted")
	}
	cfg.EnableAccounts, cfg.EnableMetrics, cfg.EnableImages, cfg.EnableJobs, cfg.EnableProbe, cfg.EnableFamilyIgnore, cfg.EnableWebhooks = false, false, false, false, false, false, false
	if _, err = contractServer(t, cfg, devBackend(), slog.New(slog.DiscardHandler), WithDevMode(newDevFixture(t, devConfig(true, true, "")).ctrl)); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("capable configuration without accounts: %v", err)
	}
}

func TestDevModeVisibleWhileActive(t *testing.T) {
	f := newDevFixture(t, devConfig(true, true, ""))
	if w := devRequest(f.handler, "GET", "/healthz", "", "", ""); w.Header().Get("X-Jelee-Dev-Mode") != "false" {
		t.Fatal("inactive capable instance claims developer mode")
	}
	f.enable(t)
	for _, path := range []string{"/healthz", "/api/v1/items", "/nope"} {
		if w := devRequest(f.handler, "GET", path, "", "", ""); w.Header().Get("X-Jelee-Dev-Mode") != "true" {
			t.Fatalf("%s: header %q", path, w.Header().Get("X-Jelee-Dev-Mode"))
		}
	}
	var system struct {
		Data struct {
			DevMode   bool   `json:"devMode"`
			ExpiresAt string `json:"devModeExpiresAt"`
		} `json:"data"`
	}
	w := devRequest(f.handler, "GET", "/api/v1/system", "", "", "")
	if json.Unmarshal(w.Body.Bytes(), &system) != nil || !system.Data.DevMode || system.Data.ExpiresAt != "2026-10-04T13:00:00Z" {
		t.Fatalf("system info: %s", w.Body)
	}
	// G45.7: at the deadline everything returns to production.
	f.clock.Advance(time.Hour)
	if w = devRequest(f.handler, "GET", "/api/v1/system", "", "", ""); w.Header().Get("X-Jelee-Dev-Mode") != "false" || strings.Contains(w.Body.String(), `"devMode":true`) {
		t.Fatalf("expired session still visible: %s", w.Body)
	}
}

func TestDevModeTokenEntryIsLoopbackOnly(t *testing.T) {
	f := newDevFixture(t, devConfig(true, true, ""))
	for _, tc := range []struct {
		remote  string
		headers []string
	}{
		{"192.0.2.10:40000", nil},
		{"[2001:db8::1]:40000", nil},
		{"127.0.0.1:40000", []string{"X-Forwarded-For", "198.51.100.7"}},
		{"127.0.0.1:40000", []string{"Forwarded", "for=198.51.100.7"}},
	} {
		if w := devRequest(f.handler, "POST", DevAPIPrefix+"/token", "{}", "", tc.remote, tc.headers...); w.Code != http.StatusNotFound {
			t.Fatalf("%s %v: %d", tc.remote, tc.headers, w.Code)
		}
	}
	if w := devRequest(f.handler, "POST", DevAPIPrefix+"/token", "{}", "", "[::1]:40000"); w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"token":"jdm_`) {
		t.Fatalf("IPv6 loopback: %d %s", w.Code, w.Body)
	}
	if w := devRequest(f.handler, "POST", DevAPIPrefix+"/token", `{"x":1}`, "", "127.0.0.1:40000"); w.Code != http.StatusBadRequest {
		t.Fatalf("token with a body: %d", w.Code)
	}
	issued := 0
	for _, a := range f.store.Audit() {
		if a.TokenIssued {
			issued++
		}
	}
	if issued != 1 {
		t.Fatalf("tokens issued: %d", issued)
	}
}

func TestDevModeAdministratorAPI(t *testing.T) {
	f := newDevFixture(t, devConfig(true, true, ""))
	put := func(token, toggle, body string) *httptest.ResponseRecorder {
		return devRequest(f.handler, "PUT", DevAPIPrefix+"/toggles/"+toggle, body, token, "192.0.2.10:40000")
	}
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", DevAPIPrefix, "", 401}, {"GET", DevAPIPrefix, "v", 403},
		{"POST", DevAPIPrefix + "/disable", "v", 403}, {"POST", DevAPIPrefix + "/disable", "a", 409},
	} {
		if w := devRequest(f.handler, tc.method, tc.path, map[string]string{"GET": "", "POST": "{}"}[tc.method], tc.token, ""); w.Code != tc.status {
			t.Fatalf("%s %s as %q: %d %s", tc.method, tc.path, tc.token, w.Code, w.Body)
		}
	}
	if w := put("v", string(devmode.RelaxHostStrict), `{"enabled":true,"iUnderstand":true}`); w.Code != 403 {
		t.Fatalf("viewer toggle: %d", w.Code)
	}
	if w := put("a", string(devmode.RelaxHostStrict), `{"enabled":true,"iUnderstand":true}`); w.Code != 409 || !strings.Contains(w.Body.String(), "devmode_inactive") {
		t.Fatalf("toggle while inactive: %d %s", w.Code, w.Body)
	}
	f.enable(t)
	for _, tc := range []struct {
		toggle, body, code string
		status             int
	}{
		{string(devmode.RelaxHostStrict), `{"enabled":true}`, "confirmation_required", 400},
		{string(devmode.DebugTranscode), `{"enabled":true,"iUnderstand":true}`, "devmode_toggle_unavailable", 409},
		{"relax_everything", `{"enabled":true}`, "not_found", 404},
		{string(devmode.RelaxHostStrict), `{}`, "invalid_request", 400},
		{string(devmode.RelaxHostStrict), `{"enabled":true,"extra":1}`, "invalid_request", 400},
	} {
		if w := put("a", tc.toggle, tc.body); w.Code != tc.status || !strings.Contains(w.Body.String(), `"`+tc.code+`"`) {
			t.Fatalf("%s %s: %d %s", tc.toggle, tc.body, w.Code, w.Body)
		}
	}
	// Host relaxation, one toggle at a time.
	if w := devRequest(f.handler, "GET", "/healthz", "", "", "", "Host", "lab.example:8097"); w.Code != 400 {
		t.Fatalf("host before relaxation: %d", w.Code)
	}
	w := put("a", string(devmode.RelaxHostStrict), `{"enabled":true,"iUnderstand":true}`)
	var state struct {
		Data devStateView `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || !state.Data.Active || len(state.Data.Toggles) != len(devmode.Toggles()) {
		t.Fatalf("toggle on: %d %s", w.Code, w.Body)
	}
	for _, view := range state.Data.Toggles {
		if view.Enabled != (view.Name == string(devmode.RelaxHostStrict)) || view.Available != (view.Name == string(devmode.RelaxHostStrict) || view.Name == string(devmode.RelaxLoginRateLimit) || view.Name == string(devmode.DebugBodyLogging) || view.Name == string(devmode.DebugPprof)) {
			t.Fatalf("toggle view %+v", view)
		}
	}
	if w = devRequest(f.handler, "GET", "/healthz", "", "", "", "Host", "lab.example:8097"); w.Code != 200 {
		t.Fatalf("relaxed host: %d", w.Code)
	}
	if w = devRequest(f.handler, "GET", "/healthz", "", "", "", "Host", "bad host"); w.Code != 400 {
		t.Fatalf("malformed host under relaxation: %d", w.Code)
	}
	// Switching off needs no confirmation and restores the check.
	if w = put("a", string(devmode.RelaxHostStrict), `{"enabled":false}`); w.Code != 200 {
		t.Fatalf("toggle off: %d %s", w.Code, w.Body)
	}
	if w = devRequest(f.handler, "GET", "/healthz", "", "", "", "Host", "lab.example:8097"); w.Code != 400 {
		t.Fatalf("host after toggle off: %d", w.Code)
	}
	if w = devRequest(f.handler, "GET", DevAPIPrefix, "", "a", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"active":true`) {
		t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	if w = put("a", string(devmode.RelaxHostStrict), `{"enabled":true,"iUnderstand":true}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = devRequest(f.handler, "POST", DevAPIPrefix+"/disable", "{}", "a", ""); w.Code != 204 {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if w = devRequest(f.handler, "GET", "/healthz", "", "", "", "Host", "lab.example:8097"); w.Code != 400 || w.Header().Get("X-Jelee-Dev-Mode") != "false" {
		t.Fatalf("after disable: %d", w.Code)
	}
	var kinds []devmode.EventKind
	for _, a := range f.store.Audit() {
		if !a.TokenIssued {
			kinds = append(kinds, a.Event.Kind)
		}
	}
	// Denials (confirmation, inactive) are audited alongside the changes.
	want := []devmode.EventKind{devmode.EventDenied, devmode.EventEnabled, devmode.EventDenied, devmode.EventToggleChanged, devmode.EventToggleChanged, devmode.EventToggleChanged, devmode.EventDisabled}
	if len(kinds) != len(want) {
		t.Fatalf("audit %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("audit %v", kinds)
		}
	}
}

func TestDevModePprofGate(t *testing.T) {
	f := newDevFixture(t, devConfig(true, true, ""))
	get := func(token, remote string) int {
		return devRequest(f.handler, "GET", "/debug/pprof/cmdline", "", token, remote).Code
	}
	if get("", "127.0.0.1:40000") != 404 || get("a", "192.0.2.1:1") != 404 {
		t.Fatal("pprof served without a session")
	}
	f.enable(t)
	if get("", "127.0.0.1:40000") != 404 {
		t.Fatal("pprof served without its toggle")
	}
	if _, err := f.ctrl.SetToggle(context.Background(), devmode.Actor{}, devmode.DebugPprof, true, devmode.Confirmation{IUnderstand: true}, "test"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token, remote string
		status        int
	}{
		{"", "127.0.0.1:40000", 200}, {"a", "192.0.2.1:1", 200},
		{"v", "192.0.2.1:1", 404}, {"", "192.0.2.1:1", 401},
	} {
		if got := get(tc.token, tc.remote); got != tc.status {
			t.Fatalf("%q from %s: %d", tc.token, tc.remote, got)
		}
	}
	if w := devRequest(f.handler, "GET", "/debug/pprof/cmdline", "", "", "127.0.0.1:40000", "X-Forwarded-For", "198.51.100.1"); w.Code != 401 {
		t.Fatalf("proxied loopback treated as local: %d", w.Code)
	}
	// Only the pprof prefix is routed; other debug paths stay hidden.
	if w := devRequest(f.handler, "GET", "/api/v1/debug/pprof/heap", "", "", "127.0.0.1:40000"); w.Code != 404 {
		t.Fatalf("debug path outside the prefix: %d", w.Code)
	}
	f.clock.Advance(time.Hour)
	if get("", "127.0.0.1:40000") != 404 {
		t.Fatal("pprof outlived the session")
	}
}

func TestDevModeBodyLoggingRedacts(t *testing.T) {
	f := newDevFixture(t, devConfig(true, true, ""))
	login := `{"name":"alice","password":"hunter2-secret","nested":{"apiKey":"k-123"}}`
	devRequest(f.handler, "POST", "/api/v1/auth/login", login, "", "")
	if strings.Contains(f.logs.String(), "devmode_body_log") {
		t.Fatal("bodies logged without the toggle")
	}
	f.enable(t)
	if _, err := f.ctrl.SetToggle(context.Background(), devmode.Actor{}, devmode.DebugBodyLogging, true, devmode.Confirmation{IUnderstand: true}, "test"); err != nil {
		t.Fatal(err)
	}
	f.logs.Reset()
	devRequest(f.handler, "POST", "/api/v1/auth/login", login, "", "")
	out := f.logs.String()
	if !strings.Contains(out, "devmode_body_log") || !strings.Contains(out, "alice") || !strings.Contains(out, devBodyRedacted) ||
		strings.Contains(out, "hunter2") || strings.Contains(out, "k-123") || !strings.Contains(out, "/api/v1/auth/login") {
		t.Fatalf("body log: %s", out)
	}
	f.logs.Reset()
	// A non-JSON response is reported by size only.
	devRequest(f.handler, "GET", "/api-docs", "", "", "")
	if out = f.logs.String(); !strings.Contains(out, "bytes, not logged") || strings.Contains(out, "<html") {
		t.Fatalf("html body logged: %s", out)
	}
}

func TestDevBodySummary(t *testing.T) {
	for _, tc := range []struct {
		contentType, body string
		complete          bool
		want              string
	}{
		{"application/json", `{"token":"t","items":[{"Secret":"s","title":"x"}],"refresh_token":"r"}`, true, `{"items":[{"Secret":"[redacted]","title":"x"}],"refresh_token":"[redacted]","token":"[redacted]"}`},
		{"application/problem+json; charset=utf-8", `{"csrf":"c"}`, true, `{"csrf":"[redacted]"}`},
		{"application/json", `{"a":`, true, "[5 bytes, invalid JSON, not logged]"},
		{"application/json", `{"a":1}`, false, "[7 bytes JSON exceeds the capture limit, not logged]"},
		{"video/mp4", "data", true, "[4 bytes, not logged]"},
		{"", "", true, ""},
	} {
		if got := devBodySummary(tc.contentType, []byte(tc.body), int64(len(tc.body)), tc.complete); got != tc.want {
			t.Errorf("%s %s: %s", tc.contentType, tc.body, got)
		}
	}
}

func TestLoginLimiterRelaxation(t *testing.T) {
	relaxed := false
	l, err := NewLoginLimiter(LoginLimiterOptions{Window: time.Minute, IPLimit: 1, UserLimit: 1, MaxEntries: 4, Relaxed: func() bool { return relaxed }})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := l.Allow("192.0.2.1", "a"); !ok {
		t.Fatal("first attempt")
	}
	if ok, _ := l.Allow("192.0.2.1", "a"); ok {
		t.Fatal("limit not applied")
	}
	relaxed = true
	for range 5 {
		if ok, _ := l.Allow("192.0.2.1", "a"); !ok {
			t.Fatal("relaxed limiter refused")
		}
	}
	if ok, _ := l.Allow("not an ip", "a"); ok {
		t.Fatal("relaxation admitted a malformed address")
	}
	relaxed = false
	if ok, _ := l.Allow("192.0.2.1", "a"); ok {
		t.Fatal("limit not restored")
	}
}

// The developer specification documents exactly the developer routes.
func TestDevModeOpenAPIMatchesRoutes(t *testing.T) {
	cfg := devConfig(true, true, "")
	registered := registeredRoutes(t, newDevFixture(t, cfg).handler)
	for route := range undocumentedRoutes {
		delete(registered, route)
	}
	documented := documentedRoutes(Specification(cfg))
	if missing := sortedDifference(registered, documented); len(missing) != 0 {
		t.Errorf("registered routes missing from OpenAPI: %v", missing)
	}
	if phantom := sortedDifference(documented, registered); len(phantom) != 0 {
		t.Errorf("OpenAPI paths without a registered route: %v", phantom)
	}
	ops := 0
	for path, item := range Specification(cfg)["paths"].(map[string]any) {
		if strings.HasPrefix(path, DevAPIPrefix) || strings.HasPrefix(path, "/debug/") {
			for _, op := range item.(map[string]any) {
				if op.(map[string]any)["x-jelee-dev-only"] != true {
					t.Errorf("%s is not marked developer only", path)
				}
				ops++
			}
		}
	}
	if ops != 6 {
		t.Fatalf("developer operations documented: %d", ops)
	}
}

// relax_client_ua_block suspends deny and pending verdicts, and
// relax_api_rate_limit suspends client rate limits (G45.4); both end with
// the toggle.
func TestClientControlDeveloperRelaxations(t *testing.T) {
	store := &gateStore{}
	limited := gateRule("22222222-2222-4222-8222-222222222222", "rate_limit", "Hammer/1")
	limited.RateLimit = &domain.ClientRuleRate{Requests: 1, PeriodSeconds: 3600}
	store.set(1, "allow", gateRule("11111111-1111-4111-8111-111111111111", "deny", "Bad/1"), limited)
	gate := newGate(t, store, nil, ClientControlOptions{})
	f := newDevFixture(t, devConfig(true, true, ""))
	gate.dev = f.ctrl
	h := gateHandler(store, gate)
	if w := gateRequest(h, http.MethodGet, "Bad/1"); w.Code != http.StatusForbidden {
		t.Fatalf("deny before relaxation: %d", w.Code)
	}
	gateRequest(h, http.MethodGet, "Hammer/1")
	if w := gateRequest(h, http.MethodGet, "Hammer/1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit before relaxation: %d", w.Code)
	}
	f.enable(t)
	// The fixture controller only wires a few toggles; this one is wired here.
	ctrl, err := devmode.NewController(devmode.ControllerOptions{Store: f.store, Clock: f.clock, Local: devmode.Inputs{EnvFlag: true, ConfigEnabled: true}, Available: devmode.Toggles()})
	if err != nil {
		t.Fatal(err)
	}
	gate.dev = ctrl
	for _, toggle := range []devmode.Toggle{devmode.RelaxClientUABlock, devmode.RelaxAPIRateLimit} {
		if _, err = ctrl.SetToggle(context.Background(), devmode.Actor{}, toggle, true, devmode.Confirmation{}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if w := gateRequest(h, http.MethodGet, "Bad/1"); w.Code != http.StatusNoContent {
		t.Fatalf("deny under relaxation: %d", w.Code)
	}
	for range 3 {
		if w := gateRequest(h, http.MethodGet, "Hammer/1"); w.Code != http.StatusNoContent {
			t.Fatalf("rate limit under relaxation: %d", w.Code)
		}
	}
	f.clock.Advance(time.Hour)
	if w := gateRequest(h, http.MethodGet, "Bad/1"); w.Code != http.StatusForbidden {
		t.Fatalf("deny after expiry: %d", w.Code)
	}
	if w := gateRequest(h, http.MethodGet, "Hammer/1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit after expiry: %d", w.Code)
	}
}
