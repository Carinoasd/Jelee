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
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/go-chi/chi/v5"
)

type httpJobRepo struct {
	app.JobRepository
	submit func(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error)
	list   func(context.Context, domain.Actor, string, int, string) ([]domain.Job, error)
	cancel func(context.Context, domain.Actor, string) (domain.Job, error)
}

func (r httpJobRepo) SubmitJob(ctx context.Context, a domain.Actor, id, key, p string, policy domain.JobPolicy) (domain.Job, bool, error) {
	return r.submit(ctx, a, id, key, p, policy)
}
func (r httpJobRepo) ListJobs(ctx context.Context, a domain.Actor, c string, n int, s string) ([]domain.Job, error) {
	return r.list(ctx, a, c, n, s)
}
func (r httpJobRepo) CancelJob(ctx context.Context, a domain.Actor, id string) (domain.Job, error) {
	return r.cancel(ctx, a, id)
}

func newJobsHTTP(t *testing.T, repo httpJobRepo, enabled bool) http.Handler {
	t.Helper()
	jobs, err := app.NewJobs(repo, config.DefaultJobsConfig().Policy())
	if err != nil {
		t.Fatal(err)
	}
	return newJobsHTTPService(t, jobs, enabled)
}

func newJobsHTTPService(t *testing.T, jobs *app.Jobs, enabled bool) http.Handler {
	t.Helper()
	cfg := validConfig()
	cfg.EnableAccounts = true
	cfg.Accounts = config.DefaultAccountsConfig()
	cfg.EnableJobs = enabled
	cfg.Jobs = config.DefaultJobsConfig()
	cfg.MaxConnections = 8
	accounts, err := app.NewAccounts(httpAccountRepository{}, &httpAccountPasswords{}, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{auth: func(ctx context.Context, token string) (access.Principal, error) {
		if token == strings.Repeat("a", 43) || token == strings.Repeat("u", 43) {
			return access.Principal{UserID: userID, SessionID: sessionID, Admin: token == strings.Repeat("a", 43), Locale: "zh-TW"}, nil
		}
		return access.Principal{}, domain.ErrUnauthenticated
	}}
	h, err := NewWithJobs(cfg, backend, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, jobs)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func jobRequest(h http.Handler, method, path, body, token string, keys ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+strings.Repeat(token, 43))
	}
	r.Header.Set("Content-Type", "application/json")
	for _, key := range keys {
		r.Header.Add("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestJobSubmissionAuthorizationAndStrictContract(t *testing.T) {
	calls := 0
	replay := false
	repo := httpJobRepo{submit: func(ctx context.Context, a domain.Actor, id, key, p string, policy domain.JobPolicy) (domain.Job, bool, error) {
		calls++
		if a.UserID != userID || a.SessionID != sessionID || id != libraryID || key != "scan-1" || p != "manual" || policy.MaxEntries != 100000 {
			t.Fatal("untrusted submission inputs")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		return domain.Job{ID: itemID, LibraryID: libraryID, State: "queued"}, replay, nil
	}}
	h := newJobsHTTP(t, repo, true)
	path := "/api/v1/libraries/" + libraryID + "/scan"
	for _, tc := range []struct {
		body, token string
		keys        []string
		status      int
	}{
		{"{}", "", []string{"scan-1"}, 401}, {"{}", "u", []string{"scan-1"}, 403},
		{"{}", "a", nil, 400}, {"{}", "a", []string{"scan-1", "scan-2"}, 400},
		{`{"root":"/private/root"}`, "a", []string{"scan-1"}, 400}, {`{"priority":"manual","Priority":"background"}`, "a", []string{"scan-1"}, 400},
		{`{"priority":null}`, "a", []string{"scan-1"}, 400}, {`{"priority":"bogus"}`, "a", []string{"scan-1"}, 400},
		{strings.Repeat(" ", 65537), "a", []string{"scan-1"}, 413},
	} {
		w := jobRequest(h, "POST", path, tc.body, tc.token, tc.keys...)
		if w.Code != tc.status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body)
		}
	}
	if calls != 0 {
		t.Fatal("invalid requests reached repository")
	}
	w := jobRequest(h, "POST", path, "{}", "a", "scan-1")
	if w.Code != 202 || w.Header().Get("Location") != "/api/v1/jobs/"+itemID {
		t.Fatalf("accepted %d %s", w.Code, w.Body)
	}
	replay = true
	w = jobRequest(h, "POST", path, "{}", "a", "scan-1")
	if w.Code != 200 || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("replay contract")
	}
	w = jobRequest(h, "POST", path+"?root=secret", "{}", "a", "scan-1")
	if w.Code != 400 || calls != 2 {
		t.Fatal("query reached submission")
	}
}
func TestJobPaginationAndCancel(t *testing.T) {
	calls := 0
	h := newJobsHTTP(t, httpJobRepo{list: func(ctx context.Context, a domain.Actor, c string, n int, s string) ([]domain.Job, error) {
		calls++
		if c != itemID || n != 1 || s != "failed" {
			t.Fatal("pagination")
		}
		return []domain.Job{{ID: sourceID}}, nil
	}, cancel: func(ctx context.Context, a domain.Actor, id string) (domain.Job, error) {
		calls++
		return domain.Job{ID: id, State: "cancelled"}, nil
	}}, true)
	for _, query := range []string{"limit=0", "limit=101", "limit=1&limit=2", "cursor=wrong", "state=wrong", "root=x", "limit=%zz", "limit="} {
		if w := jobRequest(h, "GET", "/api/v1/jobs?"+query, "", "a"); w.Code != 400 {
			t.Fatalf("query %s=%d", query, w.Code)
		}
	}
	w := jobRequest(h, "GET", "/api/v1/jobs?cursor="+itemID+"&limit=1&state=failed", "", "a")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"nextCursor":"`+sourceID+`"`) {
		t.Fatal("page response")
	}
	for _, body := range []string{"{}"} {
		w = jobRequest(h, "POST", "/api/v1/jobs/"+itemID+"/cancel", body, "a")
		if w.Code != 200 {
			t.Fatalf("cancel %d", w.Code)
		}
	}
	if calls != 2 {
		t.Fatal("unexpected repository calls")
	}
}
func TestJobErrorsAreSafeAndTranslated(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{domain.ErrJobQueueFull, 429, "job_queue_full"}, {domain.ErrJobBusy, 409, "job_busy"}, {domain.ErrScanUnavailable, 503, "scan_unavailable"}, {domain.ErrScanLimit, 409, "scan_limit"}, {errors.New("/private/password=secret"), 500, "internal_error"}} {
		h := newJobsHTTP(t, httpJobRepo{submit: func(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error) {
			return domain.Job{}, false, tc.err
		}}, true)
		w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+"/scan", "{}", "a", "scan-1")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "secret") || w.Header().Get("Content-Language") != "zh-TW" {
			t.Fatalf("unsafe error %d %s", w.Code, w.Body)
		}
	}
}
func TestJobsRolloutOpenAPIAndAdmission(t *testing.T) {
	h := newJobsHTTP(t, httpJobRepo{}, false)
	if w := jobRequest(h, "GET", "/api/v1/jobs", "", "a"); w.Code != 404 {
		t.Fatal("disabled route exists")
	}
	cfg := validConfig()
	cfg.EnableAccounts = true
	cfg.EnableJobs = true
	cfg.Accounts = config.DefaultAccountsConfig()
	cfg.Jobs = config.DefaultJobsConfig()
	cfg.MaxConnections = 8
	paths := Specification(cfg)["paths"].(map[string]any)
	routes := map[string]bool{}
	if err := chi.Walk(newJobsHTTP(t, httpJobRepo{}, true).(chi.Routes), func(method, path string, h http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		routes[method+" "+path] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for path, value := range paths {
		for method := range value.(map[string]any) {
			if !routes[strings.ToUpper(method)+" "+path] {
				t.Fatalf("missing route %s %s", method, path)
			}
		}
	}
	if _, err := json.Marshal(Specification(cfg)); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg, jobSlots: make(chan struct{}, 1)}
	s.jobSlots <- struct{}{}
	w := httptest.NewRecorder()
	s.jobBudget(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("saturated request admitted") })).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/v1/jobs", nil))
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("missing bounded admission")
	}
}
