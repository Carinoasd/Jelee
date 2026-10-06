package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/go-chi/chi/v5"
)

// lockedBuffer is a goroutine-safe sink for the asynchronous log router.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	b.buf.Reset()
	return s
}

// fakeLogStore keeps the stored settings in memory and records audits.
type fakeLogStore struct {
	mu        sync.Mutex
	settings  domain.LogSettings
	audit     domain.AuditRetention
	changes   []domain.LogLevelOverride
	resets    int
	refused   []string
	retention []domain.LogRetention
}

func newFakeLogStore() *fakeLogStore {
	return &fakeLogStore{settings: domain.LogSettings{LogDays: 30, LogMaxTotalMB: 2048, Overrides: []domain.LogLevelOverride{}}, audit: domain.AuditRetention{AuditDays: 365, SecurityDays: 365}}
}

func (s *fakeLogStore) AdminLogSettings(context.Context, domain.Actor) (domain.LogSettings, domain.AuditRetention, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, s.audit, nil
}

func (s *fakeLogStore) SetLogLevelOverride(_ context.Context, _ domain.Actor, change domain.LogLevelOverride, reset bool) (domain.LogSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := []domain.LogLevelOverride{}
	for _, o := range s.settings.Overrides {
		if o.Component != change.Component {
			next = append(next, o)
		}
	}
	if reset {
		s.resets++
	} else {
		s.changes = append(s.changes, change)
		next = append(next, change)
	}
	s.settings.Overrides = next
	return s.settings, nil
}

func (s *fakeLogStore) RecordLogLevelRefused(_ context.Context, _ domain.Actor, component, level string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused = append(s.refused, component+"="+level)
	return nil
}

func (s *fakeLogStore) SetLogRetention(_ context.Context, _ domain.Actor, r domain.LogRetention) (domain.LogSettings, domain.AuditRetention, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retention = append(s.retention, r)
	s.settings.LogDays, s.settings.LogMaxTotalMB = r.LogDays, r.LogMaxTotalMB
	s.audit.AuditDays, s.audit.SecurityDays = r.AuditDays, r.SecurityDays
	return s.settings, s.audit, nil
}

type loggingHTTP struct {
	handler http.Handler
	router  *logging.Router
	out     *lockedBuffer
	store   *fakeLogStore
}

func (f *loggingHTTP) logs(t *testing.T) []map[string]any {
	t.Helper()
	if err := f.router.Flush(); err != nil {
		t.Fatal(err)
	}
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(f.out.take()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q", line)
		}
		recs = append(recs, rec)
	}
	return recs
}

func findLog(recs []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, rec := range recs {
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// newLoggingHTTP builds a server whose logger is a real log router, so
// every assertion sees records after the production whitelist.
func newLoggingHTTP(t *testing.T, production bool) *loggingHTTP {
	t.Helper()
	out := &lockedBuffer{}
	router, err := logging.Open(logging.Options{BufferEntries: 1 << 12}, out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Close() })
	cfg := validConfig()
	cfg.EnableAccounts = true
	cfg.Accounts = config.DefaultAccountsConfig()
	cfg.EnableCatalog = true
	if production {
		cfg.Dev.Environment = "production"
	}
	accounts, err := app.NewAccounts(httpAccountRepository{}, &httpAccountPasswords{}, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{auth: func(_ context.Context, token string) (access.Principal, error) {
		if token == strings.Repeat("a", 43) || token == strings.Repeat("u", 43) {
			return access.Principal{UserID: userID, SessionID: sessionID, Admin: token == strings.Repeat("a", 43), Locale: "zh-TW"}, nil
		}
		return access.Principal{}, domain.ErrUnauthenticated
	}}
	store := newFakeLogStore()
	options := []Option{WithLogging(store, router)}
	h, err := newServer(cfg, backend, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, router.Logger(), accounts, nil, nil, nil, nil, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	return &loggingHTTP{handler: h, router: router, out: out, store: store}
}

func TestLoggingLevelRoutesAreAdministratorOnly(t *testing.T) {
	f := newLoggingHTTP(t, false)
	for _, path := range []string{"/api/v1/admin/logging/levels", "/api/v1/admin/logging/retention"} {
		if w := jobRequest(f.handler, http.MethodGet, path, "", ""); w.Code != 401 {
			t.Fatalf("%s anonymous: %d", path, w.Code)
		}
		if w := jobRequest(f.handler, http.MethodGet, path, "", "u"); w.Code != 403 {
			t.Fatalf("%s user: %d", path, w.Code)
		}
		if w := jobRequest(f.handler, http.MethodPut, path, `{"component":"http","level":"debug"}`, "u"); w.Code != 403 {
			t.Fatalf("%s user put: %d", path, w.Code)
		}
	}
	if len(f.store.changes) != 0 || len(f.store.retention) != 0 {
		t.Fatal("refused requests changed settings")
	}
}

// G46.2: a level change is stored, applied at once and visible in the view;
// an alias addresses its scope; reset removes it.
func TestLoggingLevelHotChange(t *testing.T) {
	f := newLoggingHTTP(t, false)
	f.router.Logger().Debug("before change", "component", "scan")
	w := jobRequest(f.handler, http.MethodPut, "/api/v1/admin/logging/levels", `{"component":"ignore","level":"debug","ttlSeconds":600}`, "a")
	if w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	view := decodeRepair[logLevelsView](t, w.Body.String())
	var scan logScopeView
	for _, c := range view.Components {
		if c.Name == "scan" {
			scan = c
		}
	}
	if scan.Effective != "debug" || scan.Override == nil || scan.Override.ExpiresAt == nil || time.Until(*scan.Override.ExpiresAt) > 10*time.Minute || view.Global.Name != "global" {
		t.Fatalf("view %+v scan %+v", view, scan)
	}
	if len(f.store.changes) != 1 || f.store.changes[0].Component != "scan" || f.store.changes[0].Level != "debug" {
		t.Fatalf("stored %+v", f.store.changes)
	}
	f.router.Logger().Debug("after change", "component", "scan")
	recs := f.logs(t)
	if len(findLog(recs, "before change")) != 0 || len(findLog(recs, "after change")) != 1 {
		t.Fatalf("hot change not effective: %v", recs)
	}
	changed := findLog(recs, "log level changed by an administrator")
	if len(changed) != 1 || changed[0]["component"] != "audit" || changed[0]["scope"] != "scan" || changed[0]["logLevel"] != "debug" || changed[0]["userId"] != userID {
		t.Fatalf("change record %v", changed)
	}
	if w := jobRequest(f.handler, http.MethodPut, "/api/v1/admin/logging/levels", `{"component":"scan","level":"reset"}`, "a"); w.Code != 200 || f.store.resets != 1 || f.router.Level("scan") != f.router.Level(logging.GlobalComponent) {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"component":"nowhere","level":"debug"}`, `{"component":"http","level":"verbose"}`, `{"component":"http"}`, `{"level":"info"}`,
		`{"component":"http","level":"reset","ttlSeconds":5}`, `{"component":"http","level":"info","ttlSeconds":-1}`, `{"component":"http","level":"info","ttlSeconds":86401}`, `{"component":"http","level":"info","extra":1}`} {
		if w := jobRequest(f.handler, http.MethodPut, "/api/v1/admin/logging/levels", body, "a"); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if w := jobRequest(f.handler, http.MethodPut, "/api/v1/admin/logging/levels", `{"component":"global","level":"warn"}`, "a"); w.Code != 200 || f.router.Level(logging.GlobalComponent) != 4 {
		t.Fatalf("global: %d level %v", w.Code, f.router.Level(logging.GlobalComponent))
	}
	if last := f.store.changes[len(f.store.changes)-1]; last.Component != "" || !last.ExpiresAt.IsZero() {
		t.Fatalf("global outside production must not expire: %+v", last)
	}
}

// G46.8: in production DEBUG is temporary: without ttlSeconds it lasts an
// hour, longer than four hours is refused; other levels may be permanent.
func TestLoggingProductionDebugExpires(t *testing.T) {
	f := newLoggingHTTP(t, true)
	w := jobRequest(f.handler, http.MethodGet, "/api/v1/admin/logging/levels", "", "a")
	if view := decodeRepair[logLevelsView](t, w.Body.String()); w.Code != 200 || !view.Production || view.DebugMaxTTLSeconds != 4*3600 {
		t.Fatalf("view: %d %s", w.Code, w.Body)
	}
	if w := jobRequest(f.handler, http.MethodPut, "/api/v1/admin/logging/levels", `{"component":"global","level":"debug"}`, "a"); w.Code != 200 {
		t.Fatalf("debug: %d %s", w.Code, w.Body)
	}
	if got := f.store.changes[0].ExpiresAt; got.IsZero() || time.Until(got) > time.Hour || time.Until(got) < 59*time.Minute {
		t.Fatalf("production DEBUG expiry %v", got)
	}
	if w := jobRequest(f.handler, http.MethodPut, "/api/v1/admin/logging/levels", `{"component":"http","level":"debug","ttlSeconds":14401}`, "a"); w.Code != 400 {
		t.Fatalf("over four hours: %d", w.Code)
	}
	if w := jobRequest(f.handler, http.MethodPut, "/api/v1/admin/logging/levels", `{"component":"http","level":"warn"}`, "a"); w.Code != 200 || !f.store.changes[len(f.store.changes)-1].ExpiresAt.IsZero() {
		t.Fatalf("warn: %d %+v", w.Code, f.store.changes)
	}
}

// G46.10: lowering or disabling the audit or security log is refused with
// 409, audited as a security event and logged in the security log; the
// level does not change.
func TestLoggingMandatoryScopesRefused(t *testing.T) {
	f := newLoggingHTTP(t, false)
	for _, body := range []string{`{"component":"audit","level":"error"}`, `{"component":"security","level":"warn"}`, `{"component":"devmode","level":"error"}`, `{"component":"audit","level":"reset"}`} {
		w := jobRequest(f.handler, http.MethodPut, "/api/v1/admin/logging/levels", body, "a")
		var problem struct{ Error struct{ Code string } }
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != 409 || problem.Error.Code != "log_component_mandatory" {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if strings.Join(f.store.refused, ",") != "audit=error,security=warn,security=error,audit=reset" || len(f.store.changes) != 0 {
		t.Fatalf("refusals %v changes %v", f.store.refused, f.store.changes)
	}
	if f.router.Level("audit") != 0 || f.router.Level("security") != 0 {
		t.Fatal("mandatory level changed")
	}
	recs := f.logs(t)
	refused := findLog(recs, "attempt to lower or disable a mandatory log refused")
	if len(refused) != 4 || refused[0]["component"] != "security" || refused[0]["event"] != "log_disable_refused" || refused[0]["scope"] != "audit" || refused[0]["level"] != "ERROR" {
		t.Fatalf("security records %v", refused)
	}
	view := decodeRepair[logLevelsView](t, jobRequest(f.handler, http.MethodGet, "/api/v1/admin/logging/levels", "", "a").Body.String())
	mandatory := 0
	for _, c := range view.Components {
		if c.Mandatory {
			mandatory++
			if c.Effective != "info" || c.Override != nil {
				t.Fatalf("%+v", c)
			}
		}
	}
	if mandatory != 2 {
		t.Fatalf("mandatory scopes %d", mandatory)
	}
}

// G46.9: the retention route reads and replaces both retentions.
func TestLoggingRetentionRoutes(t *testing.T) {
	f := newLoggingHTTP(t, false)
	w := jobRequest(f.handler, http.MethodGet, "/api/v1/admin/logging/retention", "", "a")
	if got := decodeRepair[logRetentionView](t, w.Body.String()); w.Code != 200 || got.LogDays != 30 || got.AuditDays != 365 || got.FileLogging {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	w = jobRequest(f.handler, http.MethodPut, "/api/v1/admin/logging/retention", `{"logDays":14,"logMaxTotalMB":512,"auditDays":400,"securityDays":90}`, "a")
	if got := decodeRepair[logRetentionView](t, w.Body.String()); w.Code != 200 || got.LogDays != 14 || got.LogMaxTotalMB != 512 || got.AuditDays != 400 || got.SecurityDays != 90 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"logDays":14,"logMaxTotalMB":512,"auditDays":400}`, `{"logDays":-1,"logMaxTotalMB":512,"auditDays":400,"securityDays":90}`,
		`{"logDays":3651,"logMaxTotalMB":512,"auditDays":400,"securityDays":90}`, `{"logDays":1,"logMaxTotalMB":512,"auditDays":6,"securityDays":90}`,
		`{"logDays":1,"logMaxTotalMB":1048577,"auditDays":400,"securityDays":90}`} {
		if w := jobRequest(f.handler, http.MethodPut, "/api/v1/admin/logging/retention", body, "a"); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if len(f.store.retention) != 1 {
		t.Fatalf("stored %v", f.store.retention)
	}
}

func TestLoggingRoutesWithoutWiringAreUnavailable(t *testing.T) {
	h := newRepairHTTP(t, httpRepairer(t, &httpRepairRepository{}, &httpVariantStore{}))
	if w := jobRequest(h, http.MethodGet, "/api/v1/admin/logging/levels", "", "a"); w.Code != 503 {
		t.Fatalf("unwired: %d", w.Code)
	}
}

// G46.3/G46.4: the access record names the route pattern, status, bytes,
// user and client session and marks media; handler records carry the
// identity and the item of the route; security outcomes are logged under
// the mandatory scope; a panic is archived with its masked stack.
func TestAccessSecurityAndPanicLogs(t *testing.T) {
	item := "00000000-0000-4000-8000-0000000000b1"
	f := newLoggingHTTP(t, false)
	w := jobRequest(f.handler, http.MethodGet, "/api/v1/admin/logging/levels?secret=hunter2", "", "a")
	if w.Code != 400 {
		t.Fatalf("query refused: %d", w.Code)
	}
	w = jobRequest(f.handler, http.MethodGet, "/api/v1/admin/logging/levels", "", "a")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := jobRequest(f.handler, http.MethodGet, "/api/v1/admin/logging/levels", "", "u"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := jobRequest(f.handler, http.MethodGet, "/api/v1/items/"+item, "", "a"); w.Code == 0 {
		t.Fatal("no response")
	}
	recs := f.logs(t)
	access := findLog(recs, "request completed")
	if len(access) < 4 {
		t.Fatalf("access records %v", access)
	}
	first, ok, denied, itemRec := access[0], access[1], access[2], access[3]
	if first["route"] != "/api/v1/admin/logging/levels" || first["status"] != float64(400) || first["method"] != "GET" || first["media"] != false {
		t.Fatalf("access record %v", first)
	}
	if ok["status"] != float64(200) || ok["userId"] != userID || ok["clientId"] != sessionID || ok["bytes"].(float64) <= 0 || ok["traceId"] == nil || ok["durationMs"] == nil {
		t.Fatalf("access record %v", ok)
	}
	if itemRec["route"] != "/api/v1/items/{id}" {
		t.Fatalf("item record %v", itemRec)
	}
	all, _ := json.Marshal(recs)
	if bytes.Contains(all, []byte("hunter2")) || bytes.Contains(all, []byte("secret=")) || bytes.Contains(all, []byte(item)) && !bytes.Contains(all, []byte(`"itemId":"`+item+`"`)) {
		t.Fatalf("query string or path logged: %s", all)
	}
	security := findLog(recs, "security event")
	if len(security) != 1 || security[0]["event"] != "access_denied" || security[0]["component"] != "security" || security[0]["status"] != float64(403) || security[0]["traceId"] != denied["traceId"] {
		t.Fatalf("security records %v", security)
	}
}

func TestSecurityEventsClassification(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		status  int
		code    string
		notes   []string
		want    string
	}{
		{"/api/v1/auth/login", 401, "authentication_required", nil, "login_failed"},
		{"/api/v1/auth/login", 401, "authentication_required", []string{"login_failed", "account_locked"}, "login_failed,account_locked"},
		{"/compat/Users/AuthenticateByName", 401, "authentication_required", nil, "login_failed"},
		{"/api/v1/items", 401, "authentication_required", nil, ""},
		{"/api/v1/auth/login", 429, "auth_rate_limited", nil, "login_throttled"},
		{"/api/v1/items", 403, "client_blocked", nil, "client_blocked"},
		{"/api/v1/webhooks", 400, "webhook_target_denied", nil, "ssrf_blocked"},
		{"/api/v1/users/{id}", 403, "forbidden", nil, "access_denied"},
		{"/api/v1/items", 403, "csrf_failed", nil, "csrf_failed"},
		{"/api/v1/items", 404, "not_found", nil, ""},
	} {
		if got := strings.Join(securityEvents(tc.pattern, tc.status, tc.code, tc.notes), ","); got != tc.want {
			t.Errorf("%s %d %s %v = %q, want %q", tc.pattern, tc.status, tc.code, tc.notes, got, tc.want)
		}
	}
	var l securityLimiter
	now := time.Now()
	for i := 0; i < securityBurst; i++ {
		if ok, _ := l.allow("login_failed", now); !ok {
			t.Fatal("burst refused")
		}
	}
	if ok, _ := l.allow("login_failed", now); ok {
		t.Fatal("flood allowed")
	}
	if ok, _ := l.allow("access_denied", now); !ok {
		t.Fatal("events share a budget")
	}
	if ok, suppressed := l.allow("login_failed", now.Add(time.Second)); !ok || suppressed != 1 {
		t.Fatalf("next window: %v %d", ok, suppressed)
	}
}

func TestRouteFieldsAndRecordedIdentity(t *testing.T) {
	r := chi.NewRouter()
	var got domain.LogFields
	handler := func(_ http.ResponseWriter, r *http.Request) {
		got = domain.LogFieldsFrom(identityContext(r.Context(), r, access.Principal{UserID: userID, SessionID: sessionID}, "Living Room TV"))
	}
	r.Get("/api/v1/items/{id}", handler)
	r.Get("/api/v1/libraries/{id}/jobs", handler)
	r.Get("/api/v1/jobs/{id}", handler)
	r.Get("/api/v1/collections/{id}/items/{itemId}", handler)
	id := "00000000-0000-4000-8000-0000000000c1"
	for path, want := range map[string]domain.LogFields{
		"/api/v1/items/" + id:                               {ItemID: id},
		"/api/v1/libraries/" + id + "/jobs":                 {LibraryID: id},
		"/api/v1/jobs/" + id:                                {TaskID: id},
		"/api/v1/collections/" + sessionID + "/items/" + id: {ItemID: id},
		"/api/v1/items/not-a-uuid":                          {},
	} {
		rec := &requestRecord{}
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(context.WithValue(context.Background(), requestRecordKey{}, rec))
		r.ServeHTTP(httptest.NewRecorder(), req)
		want.UserID, want.ClientID, want.DeviceID = userID, sessionID, "Living Room TV"
		if got != want {
			t.Errorf("%s: %+v want %+v", path, got, want)
		}
		if u, c, d, _ := rec.snapshot(); u != userID || c != sessionID || d != "Living Room TV" {
			t.Errorf("%s: record %s %s %s", path, u, c, d)
		}
	}
	if !mediaRoute("/api/v1/sources/{id}/stream") || !mediaRoute("/compat/Videos/{id}/stream") || mediaRoute("/api/v1/items/{id}") || mediaRoute("/compat/Items/{id}") {
		t.Fatal("media classification")
	}
}

// A recovered panic is archived with its type and masked stack; the panic
// value never reaches the log.
func TestBoundaryArchivesPanicStack(t *testing.T) {
	out := &lockedBuffer{}
	router, err := logging.Open(logging.Options{}, out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = router.Close() }()
	s := &Server{logger: router.Logger()}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			defer func() { //nolint:contextcheck // the recovered request context is passed explicitly
				if v := recover(); v != nil {
					s.logPanic(req.Context(), w, v)
				}
			}()
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/api/v1/panic/{id}", func(http.ResponseWriter, *http.Request) { panic(errors.New("secret-panic-text")) })
	registerRoutePatterns(r)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/panic/x", nil))
	if err := router.Flush(); err != nil {
		t.Fatal(err)
	}
	text := out.take()
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &rec); err != nil {
		t.Fatal(text)
	}
	stack, _ := rec["stack"].(string)
	if rec["msg"] != "request panic" || rec["panicType"] != "*errors.errorString" || rec["route"] != "/api/v1/panic/{id}" || !strings.Contains(stack, "http.TestBoundaryArchivesPanicStack(...)") || strings.Contains(text, "secret-panic-text") || strings.Contains(stack, "0x") {
		t.Fatalf("panic record %v", rec)
	}
}

// Mounted routers (the compatibility layer) register their patterns with
// the mount prefix, the form chi reports for a matched request.
func TestMountedRoutePatternsAreRegistered(t *testing.T) {
	sub := chi.NewRouter()
	var seen string
	sub.Get("/Users/{userId}/Items", func(_ http.ResponseWriter, r *http.Request) { seen = chi.RouteContext(r.Context()).RoutePattern() })
	parent := chi.NewRouter()
	parent.Mount("/compat-test", sub)
	registerRoutePatterns(parent)
	parent.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/compat-test/Users/x/Items", nil))
	if seen != "/compat-test/Users/{userId}/Items" || !logging.KnownRoute(seen) {
		t.Fatalf("pattern %q registered=%v", seen, logging.KnownRoute(seen))
	}
}
