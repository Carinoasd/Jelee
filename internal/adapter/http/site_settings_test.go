package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// siteStore is an in-memory stand-in for the site settings storage with the
// same revision rule as PostgreSQL. A nil *siteStore serves the defaults.
type siteStore struct {
	mu         sync.Mutex
	appearance domain.SiteAppearanceRecord
	plugins    domain.SitePluginsRecord
	writes     []string
	actors     []domain.Actor
	hostsErr   error
	hostReads  int
}

func newSiteStore() *siteStore {
	return &siteStore{appearance: domain.SiteAppearanceRecord{SiteAppearance: domain.DefaultSiteAppearance()}, plugins: domain.SitePluginsRecord{SitePlugins: domain.DefaultSitePlugins()}}
}

func (f httpAccountRepository) GetSiteAppearance(_ context.Context, a domain.Actor, _ bool) (domain.SiteAppearanceRecord, error) {
	if f.site == nil {
		return domain.SiteAppearanceRecord{SiteAppearance: domain.DefaultSiteAppearance()}, nil
	}
	f.site.mu.Lock()
	defer f.site.mu.Unlock()
	f.site.actors = append(f.site.actors, a)
	return f.site.appearance, nil
}

func (f httpAccountRepository) SetSiteAppearance(_ context.Context, a domain.Actor, in domain.SiteAppearance, revision int64, source string) (domain.SiteAppearanceRecord, error) {
	f.site.mu.Lock()
	defer f.site.mu.Unlock()
	f.site.actors = append(f.site.actors, a)
	if revision != domain.AnyRevision && revision != f.site.appearance.Revision {
		return f.site.appearance, domain.ErrConflict
	}
	f.site.writes = append(f.site.writes, "appearance:"+source)
	f.site.appearance = domain.SiteAppearanceRecord{SiteAppearance: in, Revision: f.site.appearance.Revision + 1, UpdatedAt: time.Unix(1, 0).UTC()}
	return f.site.appearance, nil
}

func (f httpAccountRepository) GetSitePlugins(_ context.Context, a domain.Actor, _ bool) (domain.SitePluginsRecord, error) {
	f.site.mu.Lock()
	defer f.site.mu.Unlock()
	f.site.actors = append(f.site.actors, a)
	return f.site.plugins, nil
}

func (f httpAccountRepository) SetSitePlugins(_ context.Context, a domain.Actor, in domain.SitePlugins, revision int64, source string) (domain.SitePluginsRecord, error) {
	f.site.mu.Lock()
	defer f.site.mu.Unlock()
	f.site.actors = append(f.site.actors, a)
	if revision != domain.AnyRevision && revision != f.site.plugins.Revision {
		return f.site.plugins, domain.ErrConflict
	}
	f.site.writes = append(f.site.writes, "plugins:"+source)
	f.site.plugins = domain.SitePluginsRecord{SitePlugins: in, Revision: f.site.plugins.Revision + 1, UpdatedAt: time.Unix(1, 0).UTC()}
	return f.site.plugins, nil
}

func (f httpAccountRepository) GetSiteSettings(_ context.Context, a domain.Actor) (domain.SiteAppearanceRecord, domain.SitePluginsRecord, error) {
	f.site.mu.Lock()
	defer f.site.mu.Unlock()
	f.site.actors = append(f.site.actors, a)
	return f.site.appearance, f.site.plugins, nil
}

func (f httpAccountRepository) ImportSiteSettings(ctx context.Context, a domain.Actor, appearance domain.SiteAppearance, plugins domain.SitePlugins) (domain.SiteAppearanceRecord, domain.SitePluginsRecord, error) {
	stored, err := f.SetSiteAppearance(ctx, a, appearance, domain.AnyRevision, "import")
	if err != nil {
		return stored, domain.SitePluginsRecord{}, err
	}
	storedPlugins, err := f.SetSitePlugins(ctx, a, plugins, domain.AnyRevision, "import")
	return stored, storedPlugins, err
}

func (f httpAccountRepository) SiteFontHosts(context.Context) ([]string, error) {
	if f.site == nil {
		return []string{}, nil
	}
	f.site.mu.Lock()
	defer f.site.mu.Unlock()
	f.site.hostReads++
	if f.site.hostsErr != nil {
		return nil, f.site.hostsErr
	}
	return f.site.appearance.EffectiveFontHosts(), nil
}

func decodeData[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var body struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: %v", w.Body.String(), err)
	}
	return body.Data
}

const validAppearance = `{"defaultTheme":"dark","tokens":{"light":{"color-primary":" #0f766e "},"dark":{"font-family":"\"Noto Sans\", sans-serif"}},` +
	`"customCss":"a { color: red } body { background: url(https://evil.example/leak) } @font-face { font-family: B; src: url(https://fonts.example.com/b.woff2) }",` +
	`"allowExternalFonts":true,"fontHosts":[" Fonts.Example.com "],"defaultLayout":{"home":[{"id":"libraries","visible":true},{"id":"welcome","visible":false}],"detail":[]},"revision":0}`

func TestSiteAppearanceHTTPAdministratorWritesAndUsersReadEffectiveValues(t *testing.T) {
	store := newSiteStore()
	f := newAccountHTTPFixture(t, httpAccountRepository{site: store}, nil)

	put := f.serve(accountRequest(http.MethodPut, "/api/v1/site/appearance", validAppearance, "a"))
	if put.Code != 200 || put.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}
	config := decodeData[domain.SiteAppearanceConfig](t, put)
	if config.Revision != 1 || config.FontHosts[0] != "fonts.example.com" || config.Tokens.Light["color-primary"] != "#0f766e" || config.DefaultLayout == nil || len(config.DefaultLayout.Home) != 2 {
		t.Fatalf("stored config: %+v", config)
	}
	if len(config.CSSIssues) != 1 || config.CSSIssues[0].Code != domain.CSSIssueExternalURL || !strings.Contains(config.CustomCSS, "evil.example") {
		t.Fatalf("administrator must see the raw CSS and what was removed: %+v", config)
	}

	// A user reads only the effective appearance: sanitized CSS, no raw
	// text, no revision, no sanitizer report.
	view := f.serve(accountRequest(http.MethodGet, "/api/v1/site/appearance", "", "u"))
	if view.Code != 200 {
		t.Fatalf("view: %d %s", view.Code, view.Body.String())
	}
	for _, leak := range []string{"evil.example", "customCss", "revision", "cssIssues", "updatedAt", "allowExternalFonts"} {
		if strings.Contains(view.Body.String(), leak) {
			t.Fatalf("user view exposes %q: %s", leak, view.Body.String())
		}
	}
	effective := decodeData[domain.SiteAppearanceView](t, view)
	if effective.CSS != "a{color:red}\n@font-face{font-family:B;src:url(https://fonts.example.com/b.woff2)}" || effective.DefaultTheme != "dark" || len(effective.FontHosts) != 1 {
		t.Fatalf("effective appearance: %+v", effective)
	}

	// Administrator-only operations refuse users before touching storage.
	writes := len(store.writes)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/site/appearance/config", ""},
		{http.MethodPut, "/api/v1/site/appearance", validAppearance},
		{http.MethodPost, "/api/v1/site/appearance/reset", "{}"},
		{http.MethodGet, "/api/v1/site/plugins/config", ""},
		{http.MethodPut, "/api/v1/site/plugins", `{"plugins":[],"settings":{},"revision":0}`},
		{http.MethodPost, "/api/v1/site/plugins/reset", "{}"},
		{http.MethodGet, "/api/v1/site/export", ""},
		{http.MethodPost, "/api/v1/site/import", "{}"},
	} {
		assertProblem(t, f.serve(accountRequest(c.method, c.path, c.body, "u")), 403, "forbidden")
		assertProblem(t, f.serve(accountRequest(c.method, c.path, c.body, "")), 401, "authentication_required")
	}
	if len(store.writes) != writes {
		t.Fatal("a refused request wrote settings")
	}

	// A stale revision is a conflict; the stored document stays.
	assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/site/appearance", validAppearance, "a")), 409, "conflict")
	reset := f.serve(accountRequest(http.MethodPost, "/api/v1/site/appearance/reset", "{}", "a"))
	if reset.Code != 200 || decodeData[domain.SiteAppearanceConfig](t, reset).CustomCSS != "" || store.writes[len(store.writes)-1] != "appearance:reset" {
		t.Fatalf("reset: %d %s %v", reset.Code, reset.Body.String(), store.writes)
	}
	for _, a := range store.actors {
		if a.UserID != userID || a.SessionID != sessionID {
			t.Fatalf("site settings used another identity: %+v", a)
		}
	}
}

func TestSiteAppearanceHTTPRefusesHostileOrIncompleteInput(t *testing.T) {
	store := newSiteStore()
	f := newAccountHTTPFixture(t, httpAccountRepository{site: store}, nil)
	replace := func(field, value string) string {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(validAppearance), &doc); err != nil {
			t.Fatal(err)
		}
		if value == "" {
			delete(doc, field)
		} else {
			doc[field] = json.RawMessage(value)
		}
		data, _ := json.Marshal(doc)
		return string(data)
	}
	// Structural CSS problems refuse the whole document, whatever the web
	// client did or did not check.
	for _, css := range []string{
		`"</style><script>alert(1)</script>"`,
		`"a { color: red } <img src=x onerror=alert(1)>"`,
		`"div { background: \\75rl(https://evil.example/x) }"`,
		`"div { color: red\u0000 }"`,
		`"div { color: red } /* body { background: url(https://evil.example) }"`,
		`"a { color: red } } body { background: url(https://evil.example/x) }"`,
		`"` + strings.Repeat("a{color:red}", 6000) + `"`,
	} {
		assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/site/appearance", replace("customCss", css), "a")), 400, "custom_css_rejected")
	}
	for _, body := range []string{
		replace("customCss", ""), replace("tokens", ""), replace("defaultLayout", ""), replace("revision", ""),
		replace("defaultTheme", `"sepia"`),
		replace("revision", `-1`),
		replace("tokens", `{"light":{"color-primary":"url(/x.png)"},"dark":{}}`),
		replace("tokens", `{"light":{"color-primary":"red; } body { display: none"},"dark":{}}`),
		replace("tokens", `{"light":{"--anything":"red"},"dark":{}}`),
		replace("tokens", `{"light":{}}`),
		replace("tokens", `{"light":{},"dark":null}`),
		replace("fontHosts", `["fonts.example.com; script-src *"]`),
		replace("fontHosts", `["1.2.3.4"]`),
		replace("fontHosts", `["https://fonts.example.com"]`),
		replace("fontHosts", `["a.example.com","A.example.com"]`),
		replace("fontHosts", `["a1.example.com","a2.example.com","a3.example.com","a4.example.com","a5.example.com","a6.example.com","a7.example.com","a8.example.com","a9.example.com","a10.example.com","a11.example.com"]`),
		replace("defaultLayout", `{"home":[{"id":"a","visible":true},{"id":"a","visible":false}],"detail":[]}`),
		replace("defaultLayout", `{"home":[{"id":"a"}],"detail":[]}`),
		replace("defaultLayout", `{"home":[],"detail":[],"extra":[]}`),
		replace("defaultLayout", `{"home":[{"id":"<b>","visible":true}],"detail":[]}`),
		replace("allowExternalFonts", `null`),
		replace("extra", `true`),
		`[]`, ``,
	} {
		assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/site/appearance", body, "a")), 400, "invalid_request")
	}
	if len(store.writes) != 0 {
		t.Fatalf("refused input reached storage: %v", store.writes)
	}
	// null clears the default layout.
	if w := f.serve(accountRequest(http.MethodPut, "/api/v1/site/appearance", replace("defaultLayout", "null"), "a")); w.Code != 200 || decodeData[domain.SiteAppearanceConfig](t, w).DefaultLayout != nil {
		t.Fatalf("null layout: %d %s", w.Code, w.Body.String())
	}
}

func TestSitePluginsHTTPSettingsNamespacesAndViews(t *testing.T) {
	store := newSiteStore()
	f := newAccountHTTPFixture(t, httpAccountRepository{site: store}, nil)
	body := `{"plugins":[{"id":"jelee.item-facts","enabled":false},{"id":"jelee.accent-tokens","enabled":true}],` +
		`"settings":{"jelee.accent-tokens":{"accent":"violet","rounded":true,"extra":null,"list":[1,2.5,{"k":"v"}]},"jelee.item-facts":{"externalLinks":true}},"revision":0}`
	put := f.serve(accountRequest(http.MethodPut, "/api/v1/site/plugins", body, "a"))
	if put.Code != 200 {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}
	if config := decodeData[domain.SitePluginsRecord](t, put); config.Revision != 1 || config.Plugins[0].ID != "jelee.item-facts" || string(config.Settings["jelee.accent-tokens"]) != `{"accent":"violet","rounded":true,"extra":null,"list":[1,2.5,{"k":"v"}]}` {
		t.Fatalf("config: %+v", config)
	}
	view := f.serve(accountRequest(http.MethodGet, "/api/v1/site/plugins", "", "u"))
	if view.Code != 200 || strings.Contains(view.Body.String(), "revision") || strings.Contains(view.Body.String(), "externalLinks") || !strings.Contains(view.Body.String(), `"accent":"violet"`) {
		t.Fatalf("user view must omit revision and disabled plugins' settings: %s", view.Body.String())
	}
	if !strings.Contains(view.Body.String(), `{"id":"jelee.item-facts","enabled":false}`) {
		t.Fatalf("user view must keep the plugin order and states: %s", view.Body.String())
	}
	for _, bad := range []string{
		`{"plugins":[],"settings":{}}`,
		`{"plugins":[],"revision":1}`,
		`{"plugins":[{"id":"Bad.Id","enabled":true}],"settings":{},"revision":1}`,
		`{"plugins":[{"id":"a.b","enabled":true},{"id":"a.b","enabled":false}],"settings":{},"revision":1}`,
		`{"plugins":[{"id":"a.b"}],"settings":{},"revision":1}`,
		`{"plugins":[],"settings":{"a.b":null},"revision":1}`,
		`{"plugins":[],"settings":{"a.b":[]},"revision":1}`,
		`{"plugins":[],"settings":{"a.b":"x"},"revision":1}`,
		`{"plugins":[],"settings":{"nodot":{}},"revision":1}`,
		`{"plugins":[],"settings":{"a.b":{"bad key":1}},"revision":1}`,
		`{"plugins":[],"settings":{"a.b":{"k":[[[[[[[[[[1]]]]]]]]]]}},"revision":1}`,
		`{"plugins":[],"settings":{"a.b":{"k":"` + strings.Repeat("x", 17<<10) + `"}},"revision":1}`,
		`{"plugins":[],"settings":{"a.b":{"k":1,"K":2}},"revision":1}`,
		`{"plugins":null,"settings":{},"revision":1}`,
	} {
		assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/site/plugins", bad, "a")), 400, "invalid_request")
	}
	// The document is at revision 1 now: a second write from revision 0 is stale.
	assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/site/plugins", body, "a")), 409, "conflict")
	if w := f.serve(accountRequest(http.MethodPost, "/api/v1/site/plugins/reset", "{}", "a")); w.Code != 200 || len(decodeData[domain.SitePluginsRecord](t, w).Settings) != 0 {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	if strings.Join(store.writes, ",") != "plugins:update,plugins:reset" {
		t.Fatalf("writes: %v", store.writes)
	}
}

func TestSiteSettingsHTTPExportImportRoundTrip(t *testing.T) {
	store := newSiteStore()
	f := newAccountHTTPFixture(t, httpAccountRepository{site: store}, nil)
	if w := f.serve(accountRequest(http.MethodPut, "/api/v1/site/appearance", validAppearance, "a")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := f.serve(accountRequest(http.MethodPut, "/api/v1/site/plugins", `{"plugins":[{"id":"a.b","enabled":true}],"settings":{"a.b":{"x":null}},"revision":0}`, "a")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	export := f.serve(accountRequest(http.MethodGet, "/api/v1/site/export", "", "a"))
	if export.Code != 200 {
		t.Fatal(export.Body.String())
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(export.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	document := string(envelope.Data)
	if !strings.Contains(document, `"format":"jelee.site-settings"`) || !strings.Contains(document, `"version":1`) || strings.Contains(document, `"revision"`) {
		t.Fatalf("export: %s", document)
	}
	// The exported file imports as is, onto a fresh server.
	fresh := newSiteStore()
	g := newAccountHTTPFixture(t, httpAccountRepository{site: fresh}, nil)
	imported := g.serve(accountRequest(http.MethodPost, "/api/v1/site/import", document, "a"))
	if imported.Code != 200 {
		t.Fatalf("import: %d %s", imported.Code, imported.Body.String())
	}
	result := decodeData[domain.SiteSettingsImport](t, imported)
	if result.Appearance.CustomCSS != store.appearance.CustomCSS || result.Plugins.Plugins[0].ID != "a.b" || strings.Join(fresh.writes, ",") != "appearance:import,plugins:import" {
		t.Fatalf("import result: %+v %v", result, fresh.writes)
	}
	for _, bad := range []string{
		strings.Replace(document, `"jelee.site-settings"`, `"other"`, 1),
		strings.Replace(document, `"version":1`, `"version":2`, 1),
		strings.Replace(document, `"defaultTheme":"dark"`, `"defaultTheme":"sepia"`, 1),
		`{"format":"jelee.site-settings","version":1}`,
	} {
		assertProblem(t, g.serve(accountRequest(http.MethodPost, "/api/v1/site/import", bad, "a")), 400, "invalid_request")
	}
	hostile := strings.Replace(document, `"customCss":"`, `"customCss":"\u003c/style\u003e`, 1)
	assertProblem(t, g.serve(accountRequest(http.MethodPost, "/api/v1/site/import", hostile, "a")), 400, "custom_css_rejected")
	if len(fresh.writes) != 2 {
		t.Fatalf("a refused import wrote: %v", fresh.writes)
	}
}

// cspDirectives splits a policy into directive name -> value.
func cspDirectives(t *testing.T, policy string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, part := range strings.Split(policy, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(part), " ")
		if _, dup := out[name]; dup || name == "" {
			t.Fatalf("malformed policy %q", policy)
		}
		out[name] = value
	}
	return out
}

func TestFrontendCSPAddsAllowlistedFontHostsOnly(t *testing.T) {
	store := newSiteStore()
	f := newAccountHTTPFixture(t, httpAccountRepository{site: store}, func(c *config.Config) { c.WebDir = webFixture(t) })
	base := cspDirectives(t, frontendCSP)
	if _, ok := base["font-src"]; ok {
		t.Fatal("base policy already names font-src")
	}
	shell := func() string {
		r := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("shell: %d", w.Code)
		}
		return w.Header().Get("Content-Security-Policy")
	}
	// Nothing configured: the policy is unchanged.
	if got := shell(); got != frontendCSP {
		t.Fatalf("default policy: %q", got)
	}
	// Saving through the API takes effect at once on this instance.
	if w := f.serve(accountRequest(http.MethodPut, "/api/v1/site/appearance", strings.Replace(validAppearance, `[" Fonts.Example.com "]`, `["fonts.example.com","cdn.fonts.example.net"]`, 1), "a")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	got := cspDirectives(t, shell())
	if got["font-src"] != "'self' https://fonts.example.com https://cdn.fonts.example.net" {
		t.Fatalf("font-src: %q", got["font-src"])
	}
	delete(got, "font-src")
	if len(got) != len(base) {
		t.Fatalf("directives changed: %v", got)
	}
	for name, value := range base {
		if got[name] != value {
			t.Fatalf("%s changed: %q, want %q", name, got[name], value)
		}
	}
	// The allowlist only counts while external fonts are on.
	if w := f.serve(accountRequest(http.MethodPut, "/api/v1/site/appearance", strings.Replace(strings.Replace(validAppearance, `"allowExternalFonts":true`, `"allowExternalFonts":false`, 1), `"revision":0`, `"revision":1`, 1), "a")); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if got := shell(); got != frontendCSP {
		t.Fatalf("disabled fonts kept font-src: %q", got)
	}
	// API responses never carry the frontend policy.
	if w := f.serve(accountRequest(http.MethodGet, "/api/v1/site/appearance", "", "u")); w.Header().Get("Content-Security-Policy") != apiCSP {
		t.Fatalf("API CSP: %q", w.Header().Get("Content-Security-Policy"))
	}
}

func TestFrontendCSPWithFontsNeverInjects(t *testing.T) {
	for _, hosts := range [][]string{nil, {}, {"fonts.example.com; script-src *"}, {"'unsafe-inline'"}, {"*"}, {"1.2.3.4"}, {"Fonts.Example.com"}, {"https://fonts.example.com"}, {"a b.example.com"}} {
		if got := frontendCSPWithFonts(hosts); got != frontendCSP {
			t.Errorf("%q: %q", hosts, got)
		}
	}
	many := []string{}
	for i := 0; i < 20; i++ {
		many = append(many, "f"+strings.Repeat("x", i)+".example.com")
	}
	if got := cspDirectives(t, frontendCSPWithFonts(many))["font-src"]; strings.Count(got, "https://") != domain.FontHostLimit {
		t.Fatalf("host limit: %q", got)
	}
}

func TestFrontendPolicyCachesAndSurvivesStorageErrors(t *testing.T) {
	now := time.Unix(1000, 0)
	calls := 0
	var hosts []string
	var failure error
	p := newFrontendPolicy(func(context.Context) ([]string, error) {
		calls++
		return hosts, failure
	})
	p.now = func() time.Time { return now }
	// Unreadable before the first success: the base policy, retried later.
	failure = errors.New("database down")
	if got := p.header(context.Background()); got != frontendCSP || calls != 1 {
		t.Fatalf("first failure: %q %d", got, calls)
	}
	if p.header(context.Background()); calls != 1 {
		t.Fatal("retried before the retry delay")
	}
	now = now.Add(frontendPolicyRetry)
	failure, hosts = nil, []string{"fonts.example.com"}
	withFonts := frontendCSPWithFonts(hosts)
	if got := p.header(context.Background()); got != withFonts || calls != 2 {
		t.Fatalf("loaded: %q %d", got, calls)
	}
	// Cached for the TTL, then refreshed; a failed refresh keeps the last value.
	hosts = nil
	if got := p.header(context.Background()); got != withFonts || calls != 2 {
		t.Fatal("not cached")
	}
	now = now.Add(frontendPolicyTTL)
	failure = errors.New("database down")
	if got := p.header(context.Background()); got != withFonts || calls != 3 {
		t.Fatalf("failed refresh: %q %d", got, calls)
	}
	// Invalidation reloads at the next response.
	failure = nil
	p.invalidate()
	if got := p.header(context.Background()); got != frontendCSP || calls != 4 {
		t.Fatalf("after invalidate: %q %d", got, calls)
	}
}

func TestFrontendPolicyInvalidatedDuringLoadReloads(t *testing.T) {
	calls := 0
	var p *frontendPolicy
	p = newFrontendPolicy(func(context.Context) ([]string, error) {
		calls++
		if calls == 1 {
			// A write lands while the first read is in flight.
			p.invalidate()
			return []string{"old.example.com"}, nil
		}
		return []string{"new.example.com"}, nil
	})
	if got := p.header(context.Background()); !strings.Contains(got, "old.example.com") {
		t.Fatal(got)
	}
	if got := p.header(context.Background()); !strings.Contains(got, "new.example.com") || calls != 2 {
		t.Fatalf("stale value kept after a concurrent change: %q %d", got, calls)
	}
}

func TestSystemReportsServerVersion(t *testing.T) {
	h, err := New(validConfig(), &fakeBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	w := webGet(h, http.MethodGet, "/api/v1/system")
	data := decodeData[map[string]any](t, w)
	spec := Specification(validConfig())
	if data["version"] == "" || data["version"] != spec["info"].(map[string]any)["version"] {
		t.Fatalf("system version %v, OpenAPI %v", data["version"], spec["info"])
	}
}

func TestFrontendPolicyConcurrentUse(t *testing.T) {
	p := newFrontendPolicy(func(context.Context) ([]string, error) {
		time.Sleep(time.Millisecond)
		return []string{"fonts.example.com"}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if i%4 == 0 {
					p.invalidate()
				}
				if got := p.header(context.Background()); got != frontendCSP && got != frontendCSPWithFonts([]string{"fonts.example.com"}) {
					t.Errorf("unexpected policy %q", got)
				}
			}
		}(i)
	}
	wg.Wait()
}
