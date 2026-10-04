package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

const setupTestToken = "setup-token-for-tests-0123456789abcdefghijk"

// setupWizardStub models app.Setup closely enough for the adapter: a state,
// completion, and the error shapes the real service returns.
type setupWizardStub struct {
	mu         sync.Mutex
	completed  bool
	state      domain.SetupState
	statusErr  error
	statusHits int
	origins    []domain.SetupOrigin
	submitErr  error
	languages  []string
	passwords  []string
}

func completedSetupWizard() *setupWizardStub { return &setupWizardStub{completed: true} }

func (s *setupWizardStub) Status(context.Context) (domain.SetupStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusHits++
	if s.statusErr != nil {
		return domain.SetupStatus{}, s.statusErr
	}
	return domain.SetupStatus{Completed: s.completed, State: s.state}, nil
}

func (s *setupWizardStub) step(ctx context.Context, step domain.SetupStep) (domain.SetupState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.origins = append(s.origins, domain.SetupOriginFrom(ctx))
	if s.completed {
		return s.state, domain.ErrSetupCompleted
	}
	if s.submitErr != nil {
		return s.state, s.submitErr
	}
	if s.state.Current != step {
		return s.state, domain.ErrSetupStepOrder
	}
	s.state.Current++
	s.state.Version++
	return s.state, nil
}

func (s *setupWizardStub) SubmitLanguage(ctx context.Context, locale string) (domain.SetupState, error) {
	s.mu.Lock()
	s.languages = append(s.languages, locale)
	s.mu.Unlock()
	if issues := app.ValidateSetupLanguage(locale); len(issues) > 0 {
		return s.state, &app.SetupValidationError{Step: domain.SetupStepLanguage, Issues: issues}
	}
	return s.step(ctx, domain.SetupStepLanguage)
}
func (s *setupWizardStub) SubmitAdmin(ctx context.Context, _ app.SetupAdminInput, password string) (domain.SetupState, error) {
	s.mu.Lock()
	s.passwords = append(s.passwords, password)
	s.mu.Unlock()
	return s.step(ctx, domain.SetupStepAdmin)
}
func (s *setupWizardStub) SubmitDatabase(ctx context.Context) (domain.SetupState, error) {
	return s.step(ctx, domain.SetupStepDatabase)
}
func (s *setupWizardStub) SubmitMedia(ctx context.Context, _ []domain.SetupLibrary) (domain.SetupState, error) {
	return s.step(ctx, domain.SetupStepMedia)
}
func (s *setupWizardStub) SubmitTMDB(ctx context.Context, _ domain.SetupTMDB) (domain.SetupState, error) {
	return s.step(ctx, domain.SetupStepTMDB)
}
func (s *setupWizardStub) SubmitToolchain(ctx context.Context, _ bool) (domain.SetupState, error) {
	return s.step(ctx, domain.SetupStepToolchain)
}
func (s *setupWizardStub) SubmitMetadataPolicy(ctx context.Context, _ domain.SetupMetadataPolicy) (domain.SetupState, error) {
	return s.step(ctx, domain.SetupStepMetadataPolicy)
}
func (s *setupWizardStub) SubmitNetwork(ctx context.Context, _ domain.SetupNetwork) (domain.SetupState, error) {
	return s.step(ctx, domain.SetupStepNetwork)
}
func (s *setupWizardStub) Back(context.Context) (domain.SetupState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := domain.SetupBack(s.state)
	if err == nil {
		next.Version++
		s.state = next
	}
	return s.state, err
}
func (s *setupWizardStub) Finish(ctx context.Context) (domain.SetupState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.origins = append(s.origins, domain.SetupOriginFrom(ctx))
	if s.state.Current != domain.SetupStepComplete {
		return s.state, domain.ErrSetupStepOrder
	}
	at := time.Unix(1_800_000_000, 0).UTC()
	s.state.CompletedAt, s.completed = &at, true
	s.state.Version++
	return s.state, nil
}

func (s *setupWizardStub) setCompleted(completed bool) {
	s.mu.Lock()
	s.completed = completed
	s.mu.Unlock()
}

func incompleteSetupWizard() *setupWizardStub {
	return &setupWizardStub{state: domain.NewSetupState()}
}

// setupRouter is the full reference rollout with a frontend and the given wizard.
func setupRouter(t *testing.T, wizard SetupWizard, token string) http.Handler {
	t.Helper()
	cfg := ReferenceConfig()
	cfg.WebDir = webFixture(t)
	return contractRouter(t, cfg, WithSetup(wizard, token))
}

func setupRequest(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var r *http.Request
	if reader != nil {
		r = httptest.NewRequest(method, "http://localhost/", reader)
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, "http://localhost/", nil)
	}
	r.URL.Path, r.URL.RawPath = path, ""
	if token != "" {
		r.Header.Set(SetupTokenHeader, token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func problemCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body.Error.Code
}

var routeParameter = regexp.MustCompile(`\{[^}]+\}`)

// concreteRoute fills every path parameter with a value that satisfies the
// usual UUID and token shapes, so a request reaches the matching route.
func concreteRoute(pattern string) string {
	path := routeParameter.ReplaceAllString(pattern, "11111111-1111-4111-8111-111111111111")
	return strings.TrimSuffix(path, "/*")
}

// setupAllowedRoute lists what a half-initialised instance still serves.
func setupAllowedRoute(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/api/v1/system", "/api/v1/openapi.json", "/api-docs":
		return true
	}
	return setupWizardPath(path)
}

// G18.4: walk every route the full reference rollout registers (native API,
// compatibility layer, metrics, webhooks, jobs, images, ...) and require
// 503 setup_required for each one outside the explicit allow list.
func TestSetupGateBlocksEveryRegisteredRoute(t *testing.T) {
	wizard := incompleteSetupWizard()
	handler := setupRouter(t, wizard, setupTestToken)
	type route struct{ method, pattern string }
	var routes []route
	if err := chi.Walk(handler.(chi.Routes), func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, route{method, pattern})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].method+routes[i].pattern < routes[j].method+routes[j].pattern })
	blocked, allowed := 0, 0
	for _, r := range routes {
		path := concreteRoute(r.pattern)
		w := setupRequest(handler, r.method, path, "", "")
		if setupAllowedRoute(path) {
			allowed++
			if w.Code == 503 && problemCode(t, w) == "setup_required" {
				t.Errorf("%s %s: allowed route was gated", r.method, r.pattern)
			}
			continue
		}
		blocked++
		if w.Code != 503 || problemCode(t, w) != "setup_required" {
			t.Errorf("%s %s: status %d code %q, want 503 setup_required", r.method, r.pattern, w.Code, problemCode(t, w))
		}
		if w.Header().Get("X-Request-ID") == "" || w.Header().Get("Content-Language") == "" {
			t.Errorf("%s %s: gate response lacks boundary headers", r.method, r.pattern)
		}
	}
	// The reference rollout registers well over a hundred routes; a broken
	// walk must not pass vacuously.
	if blocked < 100 || allowed != 10 {
		t.Fatalf("walked %d blocked and %d allowed routes", blocked, allowed)
	}
	t.Logf("gate: %d registered routes answered 503 setup_required, %d allowed", blocked, allowed)
	// Spellings no route claims are blocked too, except the frontend shell.
	for _, c := range []struct {
		method, path string
		status       int
		code         string
	}{
		{"GET", "/COMPAT/Items", 503, "setup_required"},
		{"GET", "/compat/Unknown/Route", 503, "setup_required"},
		{"GET", "/api/v1/unknown", 503, "setup_required"},
		{"POST", "/setup", 503, "setup_required"},
		{"GET", "/api/v1/setup/../users", 503, "setup_required"},
		{"GET", "/metrics", 503, "setup_required"},
		{"HEAD", "/metrics", 503, "setup_required"},
		{"GET", "/", 200, ""},
		{"GET", "/setup", 200, ""},
		{"HEAD", "/setup", 200, ""},
		{"GET", "/assets/index-B3x_9kQd.js", 200, ""},
	} {
		w := setupRequest(handler, c.method, c.path, "", "")
		if w.Code != c.status || c.code != "" && problemCode(t, w) != c.code {
			t.Errorf("%s %s: %d %q", c.method, c.path, w.Code, problemCode(t, w))
		}
	}
	// Probes never touch setup storage.
	before := wizard.statusHits
	for _, path := range []string{"/healthz", "/api/v1/system", "/api/v1/openapi.json"} {
		if w := setupRequest(handler, "GET", path, "", ""); w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if wizard.statusHits != before {
		t.Fatal("probe routes consulted setup storage")
	}
}

// After completion everything serves normally and the wizard is gone (410).
func TestSetupGateAfterCompletion(t *testing.T) {
	handler := setupRouter(t, completedSetupWizard(), "")
	for _, c := range []struct {
		method, path string
		status       int
		code         string
	}{
		{"GET", "/api/v1/items", 401, "authentication_required"},
		{"GET", "/compat/System/Info/Public", 200, ""},
		{"GET", "/api/v1/setup/status", 410, "setup_completed"},
		{"GET", "/api/v1/setup", 410, "setup_completed"},
		{"POST", "/api/v1/setup/steps/language", 410, "setup_completed"},
		{"POST", "/api/v1/setup/complete", 410, "setup_completed"},
		{"POST", "/api/v1/setup/anything/else", 410, "setup_completed"},
	} {
		w := setupRequest(handler, c.method, c.path, setupTestToken, "")
		if w.Code != c.status || c.code != "" && problemCode(t, w) != c.code {
			t.Errorf("%s %s: %d %q", c.method, c.path, w.Code, problemCode(t, w))
		}
	}
	w := setupRequest(handler, "GET", "/readyz", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"setup":"completed"`) {
		t.Fatalf("readyz: %d %s", w.Code, w.Body.String())
	}
}

func TestSetupWizardAPIFlowTokenAndErrors(t *testing.T) {
	wizard := incompleteSetupWizard()
	handler := setupRouter(t, wizard, setupTestToken)
	if w := setupRequest(handler, "GET", "/api/v1/setup/status", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"setupRequired":true`) {
		t.Fatalf("public status: %d %s", w.Code, w.Body.String())
	}
	if w := setupRequest(handler, "GET", "/readyz", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"setup":"required"`) {
		t.Fatalf("readyz during setup: %d %s", w.Code, w.Body.String())
	}
	for _, token := range []string{"", "wrong", setupTestToken + "x", strings.ToUpper(setupTestToken)} {
		w := setupRequest(handler, "POST", "/api/v1/setup/steps/language", token, `{"locale":"zh-TW"}`)
		if w.Code != 401 || problemCode(t, w) != "setup_token_invalid" {
			t.Fatalf("token %q: %d %q", token, w.Code, problemCode(t, w))
		}
	}
	if len(wizard.languages) != 0 {
		t.Fatal("a rejected token reached the wizard")
	}
	// Validation issues are structured and never echo the input.
	w := setupRequest(handler, "POST", "/api/v1/setup/steps/language", setupTestToken, `{"locale":"xx-SECRET"}`)
	if w.Code != 400 || problemCode(t, w) != "setup_validation_failed" || strings.Contains(w.Body.String(), "SECRET") ||
		!strings.Contains(w.Body.String(), `"issues":[{"field":"locale","code":"locale_unsupported"}]`) || !strings.Contains(w.Body.String(), `"step":"language"`) {
		t.Fatalf("validation: %d %s", w.Code, w.Body.String())
	}
	if w = setupRequest(handler, "POST", "/api/v1/setup/steps/language", setupTestToken, `{"locale":"zh-TW","extra":1}`); w.Code != 400 || problemCode(t, w) != "invalid_request" {
		t.Fatalf("unknown field: %d %q", w.Code, problemCode(t, w))
	}
	if w = setupRequest(handler, "POST", "/api/v1/setup/steps/admin", setupTestToken, `{"name":"admin","password":"x"}`); w.Code != 409 || problemCode(t, w) != "setup_step_order" {
		t.Fatalf("out of order: %d %q", w.Code, problemCode(t, w))
	}
	if w = setupRequest(handler, "POST", "/api/v1/setup/steps/nope", setupTestToken, `{}`); w.Code != 404 {
		t.Fatalf("unknown step: %d", w.Code)
	}
	if w = setupRequest(handler, "POST", "/api/v1/setup/steps/complete", setupTestToken, `{}`); w.Code != 404 {
		t.Fatalf("complete is not a step: %d", w.Code)
	}
	steps := []struct{ step, body string }{
		{"language", `{"locale":"zh-TW"}`},
		{"admin", `{"name":"admin","displayName":"Admin","password":"correct horse battery"}`},
		{"database", ``},
		{"media", `{"libraries":[{"name":"Movies","path":"/srv/movies"}]}`},
		{"tmdb", `{"enabled":false}`},
		{"toolchain", `{"acceptDegraded":true}`},
		{"metadata-policy", `{"nfoRead":"read-only","nfoWrite":"off","imageFetch":false,"imageWriteBack":false}`},
		{"network", `{"mode":"local","listen":"127.0.0.1:8097","allowedHosts":["localhost"],"privacyAcknowledged":false}`},
	}
	for i, step := range steps {
		w := setupRequest(handler, "POST", "/api/v1/setup/steps/"+step.step, setupTestToken, step.body)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", step.step, w.Code, w.Body.String())
		}
		// Back after the second step and redo it: progress is kept.
		if i == 1 {
			if w = setupRequest(handler, "POST", "/api/v1/setup/back", setupTestToken, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"current":"admin"`) {
				t.Fatalf("back: %d %s", w.Code, w.Body.String())
			}
			if w = setupRequest(handler, "POST", "/api/v1/setup/steps/admin", setupTestToken, steps[1].body); w.Code != 200 {
				t.Fatalf("redo admin: %d", w.Code)
			}
		}
	}
	if strings.Contains(w.Body.String(), "correct horse") {
		t.Fatal("response echoed the password")
	}
	if w = setupRequest(handler, "GET", "/api/v1/setup", setupTestToken, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"current":"complete"`) {
		t.Fatalf("read state: %d %s", w.Code, w.Body.String())
	}
	// Still gated until the completion commits.
	if w = setupRequest(handler, "GET", "/api/v1/items", "", ""); w.Code != 503 {
		t.Fatalf("gated before complete: %d", w.Code)
	}
	if w = setupRequest(handler, "POST", "/api/v1/setup/complete", setupTestToken, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"completedAt"`) {
		t.Fatalf("complete: %d %s", w.Code, w.Body.String())
	}
	// The gate flips at once on this instance, without waiting for a recheck.
	if w = setupRequest(handler, "GET", "/api/v1/items", "", ""); w.Code != 401 {
		t.Fatalf("after complete: %d", w.Code)
	}
	if w = setupRequest(handler, "POST", "/api/v1/setup/back", setupTestToken, ""); w.Code != 410 {
		t.Fatalf("wizard after complete: %d", w.Code)
	}
	for _, origin := range wizard.origins {
		if origin.Channel != "http" || origin.IP == "" || origin.RequestID == "" {
			t.Fatalf("setup origin not propagated: %+v", origin)
		}
	}
}

func TestSetupGateRechecksAndFailsClosed(t *testing.T) {
	wizard := incompleteSetupWizard()
	handler := setupRouter(t, wizard, setupTestToken)
	if w := setupRequest(handler, "GET", "/api/v1/items", "", ""); w.Code != 503 {
		t.Fatalf("initial: %d", w.Code)
	}
	// Another instance or the CLI completes setup; within the recheck
	// interval this instance keeps refusing, then follows.
	wizard.setCompleted(true)
	deadline := time.Now().Add(5 * setupRecheckInterval)
	for {
		w := setupRequest(handler, "GET", "/api/v1/items", "", "")
		if w.Code == 401 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("gate never noticed completion: %d", w.Code)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// A storage failure is never treated as completed.
	failing := incompleteSetupWizard()
	failing.statusErr = domain.ErrDatabase
	handler = setupRouter(t, failing, setupTestToken)
	if w := setupRequest(handler, "GET", "/api/v1/items", "", ""); w.Code != 503 || problemCode(t, w) != "not_ready" {
		t.Fatalf("storage failure: %d %q", w.Code, problemCode(t, w))
	}
	if w := setupRequest(handler, "GET", "/readyz", "", ""); w.Code != 503 {
		t.Fatalf("readyz on storage failure: %d", w.Code)
	}
	if w := setupRequest(handler, "GET", "/healthz", "", ""); w.Code != 200 {
		t.Fatalf("liveness on storage failure: %d", w.Code)
	}
}

// Without the account rollout the wizard cannot create an administrator, so
// the option is ignored and nothing is gated.
func TestSetupNeedsAccountRollout(t *testing.T) {
	cfg := ReferenceConfig()
	cfg.EnableAccounts, cfg.EnableMetrics, cfg.EnableImages, cfg.EnableJobs, cfg.EnableProbe, cfg.EnableFamilyIgnore, cfg.EnableWebhooks = false, false, false, false, false, false, false
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	handler := contractRouter(t, cfg, WithSetup(incompleteSetupWizard(), setupTestToken))
	if w := setupRequest(handler, "GET", "/api/v1/setup/status", "", ""); w.Code != 404 {
		t.Fatalf("wizard mounted without accounts: %d", w.Code)
	}
	if w := setupRequest(handler, "GET", "/api/v1/items", "", ""); w.Code != 401 {
		t.Fatalf("gated without accounts: %d", w.Code)
	}
}
