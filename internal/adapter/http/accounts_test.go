package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/i18n"
	"github.com/go-chi/chi/v5"
)

type httpAccountRepository struct {
	app.AccountRepository
	credentials func(context.Context, string) (domain.Credentials, error)
	login       func(context.Context, domain.LoginInput) (domain.SessionGrant, error)
	list        func(context.Context, domain.Actor, string, int, bool) ([]domain.User, error)
	get         func(context.Context, domain.Actor, string) (domain.User, error)
	create      func(context.Context, domain.Actor, domain.UserInput, string) (domain.User, bool, error)
	update      func(context.Context, domain.Actor, string, domain.UserInput) (domain.User, error)
	delete      func(context.Context, domain.Actor, string) error
	revoke      func(context.Context, domain.Actor, string, string) error
	profile     func(context.Context, domain.Actor, domain.ProfileInput) (domain.User, error)
	libraries   func(context.Context, domain.Actor, string, []string) error
}

func (f httpAccountRepository) Credentials(ctx context.Context, name string) (domain.Credentials, error) {
	return f.credentials(ctx, name)
}
func (f httpAccountRepository) CommitLogin(ctx context.Context, input domain.LoginInput) (domain.SessionGrant, error) {
	return f.login(ctx, input)
}
func (f httpAccountRepository) ListUsers(ctx context.Context, a domain.Actor, cursor string, limit int, deleted bool) ([]domain.User, error) {
	return f.list(ctx, a, cursor, limit, deleted)
}
func (f httpAccountRepository) GetUser(ctx context.Context, a domain.Actor, id string) (domain.User, error) {
	return f.get(ctx, a, id)
}
func (f httpAccountRepository) CreateUser(ctx context.Context, a domain.Actor, input domain.UserInput, key string) (domain.User, bool, error) {
	return f.create(ctx, a, input, key)
}
func (f httpAccountRepository) UpdateUser(ctx context.Context, a domain.Actor, id string, input domain.UserInput) (domain.User, error) {
	return f.update(ctx, a, id, input)
}
func (f httpAccountRepository) DeleteUser(ctx context.Context, a domain.Actor, id string) error {
	return f.delete(ctx, a, id)
}
func (f httpAccountRepository) RevokeSession(ctx context.Context, a domain.Actor, id, sid string) error {
	return f.revoke(ctx, a, id, sid)
}
func (f httpAccountRepository) UpdateProfile(ctx context.Context, a domain.Actor, input domain.ProfileInput) (domain.User, error) {
	return f.profile(ctx, a, input)
}
func (f httpAccountRepository) ReplaceLibraryAccess(ctx context.Context, a domain.Actor, id string, ids []string) error {
	return f.libraries(ctx, a, id, ids)
}

type httpAccountPasswords struct {
	hashCalls, verifyCalls, dummyCalls int
	verify                             func(context.Context, string, string) (bool, error)
}

func (p *httpAccountPasswords) Hash(_ context.Context, _ string) (string, error) {
	p.hashCalls++
	return "server-generated-phc", nil
}
func (p *httpAccountPasswords) Verify(ctx context.Context, password, hash string) (bool, error) {
	p.verifyCalls++
	if p.verify != nil {
		return p.verify(ctx, password, hash)
	}
	return true, nil
}
func (p *httpAccountPasswords) DummyVerify(context.Context, string) error { p.dummyCalls++; return nil }

type accountHTTPFixture struct {
	handler   http.Handler
	backend   *fakeBackend
	passwords *httpAccountPasswords
	logs      *bytes.Buffer
}

func newAccountHTTPFixture(t *testing.T, repo httpAccountRepository, change func(*config.Config)) *accountHTTPFixture {
	t.Helper()
	cfg := validConfig()
	cfg.EnableAccounts = true
	cfg.Accounts = config.DefaultAccountsConfig()
	if change != nil {
		change(&cfg)
	}
	f := &accountHTTPFixture{backend: &fakeBackend{}, passwords: &httpAccountPasswords{}, logs: &bytes.Buffer{}}
	f.backend.auth = func(_ context.Context, token string) (access.Principal, error) {
		if token != strings.Repeat("a", 43) && token != strings.Repeat("u", 43) {
			return access.Principal{}, domain.ErrUnauthenticated
		}
		return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientWeb, Admin: token == strings.Repeat("a", 43)}, nil
	}
	accounts, err := app.NewAccounts(repo, f.passwords, app.AccountOptions{SessionTTL: 24 * time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.handler, err = New(cfg, f.backend, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewJSONHandler(f.logs, nil)), accounts)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func accountRequest(method, path, body, role string) *http.Request {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	r.RemoteAddr = "198.51.100.23:12345"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept-Language", "en-US")
	if role != "" {
		r.Header.Set("Authorization", "Bearer "+strings.Repeat(role, 43))
	}
	return r
}

func (f *accountHTTPFixture) serve(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func assertAccountActor(t *testing.T, a domain.Actor) {
	t.Helper()
	if a != (domain.Actor{UserID: userID, SessionID: sessionID, IP: "198.51.100.23"}) {
		t.Fatalf("actor was not derived from authenticated principal and peer: %+v", a)
	}
}

func TestAccountHTTPLoginPublicUsesPeerAndCannotSelectNativeKind(t *testing.T) {
	commits := 0
	repo := httpAccountRepository{
		credentials: func(_ context.Context, name string) (domain.Credentials, error) {
			if name != "alice" {
				t.Fatal("wrong credential lookup")
			}
			return domain.Credentials{UserID: userID, PasswordHash: "private-stored-phc", Version: 4}, nil
		},
		login: func(_ context.Context, input domain.LoginInput) (domain.SessionGrant, error) {
			commits++
			if !input.PasswordOK || input.IP != "198.51.100.23" || input.DeviceName != "browser" {
				t.Fatal("incorrect verified login input")
			}
			for _, field := range []string{"Kind", "ClientKind", "Admin"} {
				if _, exists := reflect.TypeOf(input).FieldByName(field); exists {
					t.Fatal("login repository contract must not accept client-selected privilege/kind")
				}
			}
			return domain.SessionGrant{Token: "fixture-web-token", User: domain.User{ID: userID}, Session: domain.Session{ID: sessionID, ClientKind: "web"}}, nil
		},
	}
	f := newAccountHTTPFixture(t, repo, nil)
	r := accountRequest("POST", "/api/v1/auth/login", `{"name":"alice","password":"private-password","deviceName":"browser"}`, "")
	r.Header.Set("X-Forwarded-For", "203.0.113.99")
	r.Header.Set("Forwarded", "for=203.0.113.99")
	r.Header.Set("X-Client-Kind", "native")
	r.Header.Set("User-Agent", "NativePlayer/1")
	w := f.serve(r)
	var body struct {
		Data domain.SessionGrant `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Data.Session.ClientKind != "web" || body.Data.Token != "fixture-web-token" {
		t.Fatalf("public login contract: status=%d body=%s", w.Code, w.Body.String())
	}
	if f.backend.authCalls != 0 || commits != 1 || f.passwords.verifyCalls != 1 {
		t.Fatal("public login used bearer auth or skipped password work")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response is cacheable")
	}
	for _, secret := range []string{"private-password", "private-stored-phc", "fixture-web-token"} {
		if strings.Contains(f.logs.String(), secret) {
			t.Fatal("authentication data leaked in logs")
		}
	}
	for _, extra := range []string{`"clientKind":"native"`, `"kind":"native"`, `"admin":true`, `"passwordHash":"injected"`} {
		w = f.serve(accountRequest("POST", "/api/v1/auth/login", `{"name":"alice","password":"value",`+extra+`}`, ""))
		assertProblem(t, w, 400, "invalid_request")
	}
	if commits != 1 || f.passwords.verifyCalls != 1 {
		t.Fatal("client privilege fields reached KDF or repository")
	}
}

func TestAccountHTTPRateLimitRejectsBeforeKDFWithRetryAfter(t *testing.T) {
	lookups := 0
	f := newAccountHTTPFixture(t, httpAccountRepository{credentials: func(context.Context, string) (domain.Credentials, error) {
		lookups++
		return domain.Credentials{}, domain.ErrNotFound
	}}, func(c *config.Config) {
		c.Accounts.LoginIPLimit = 1
		c.Accounts.LoginUserLimit = 1
		c.Accounts.LoginWindowSeconds = 60
	})
	first := f.serve(accountRequest("POST", "/api/v1/auth/login", `{"name":"alice","password":"wrong"}`, ""))
	assertProblem(t, first, 401, "authentication_required")
	for _, tc := range []struct{ peer, name string }{{"198.51.100.23:23456", "bob"}, {"203.0.113.2:12345", "ALICE"}} {
		r := accountRequest("POST", "/api/v1/auth/login", `{"name":"`+tc.name+`","password":"wrong"}`, "")
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", "192.0.2.55")
		w := f.serve(r)
		assertProblem(t, w, 429, "auth_rate_limited")
		retry, err := strconv.Atoi(w.Header().Get("Retry-After"))
		if err != nil || retry < 1 || retry > 60 {
			t.Fatalf("invalid Retry-After %q", w.Header().Get("Retry-After"))
		}
	}
	if lookups != 1 || f.passwords.dummyCalls != 1 || f.passwords.verifyCalls != 0 {
		t.Fatal("rate-limited request performed credential/KDF work")
	}
}

func TestAccountHTTPAdminAndAuthenticationCheckedBeforeBodyOrKDF(t *testing.T) {
	f := newAccountHTTPFixture(t, httpAccountRepository{}, nil)
	for _, path := range []string{"/api/v1/users", "/api/v1/users/" + itemID + "/restore", "/api/v1/users/" + itemID + "/unlock"} {
		for _, role := range []string{"", "u"} {
			r := accountRequest("POST", path, "not-json", role)
			w := f.serve(r)
			if role == "" {
				assertProblem(t, w, 401, "authentication_required")
			} else {
				assertProblem(t, w, 403, "forbidden")
			}
		}
	}
	if f.passwords.hashCalls != 0 || f.passwords.verifyCalls != 0 {
		t.Fatal("unauthorized administrator request reached KDF")
	}
}

func TestAccountHTTPStrictBodiesAndIdempotencyBeforeKDF(t *testing.T) {
	f := newAccountHTTPFixture(t, httpAccountRepository{}, nil)
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/api/v1/users", `{"name":"alice","password":"secret","unknown":true}`, 400},
		{"/api/v1/users", `{"name":"alice","password":"secret","passwordHash":"injected"}`, 400},
		{"/api/v1/users", `{"name":"alice","name":"bob","password":"secret"}`, 400},
		{"/api/v1/users", `{"name":"alice","NAME":"bob","password":"secret"}`, 400},
		{"/api/v1/users", `{"name":"alice","password":"secret"} {}`, 400},
		{"/api/v1/users", `null`, 400}, {"/api/v1/users", `[]`, 400},
		{"/api/v1/users", `{"name":"` + strings.Repeat("x", accountBodyLimit) + `"}`, 413},
		{"/api/v1/auth/login", `{"name":"alice","password":"` + strings.Repeat("x", accountBodyLimit) + `"}`, 413},
	} {
		r := accountRequest("POST", tc.path, tc.body, "a")
		r.Header.Set("Idempotency-Key", "test-key")
		w := f.serve(r)
		code := "invalid_request"
		if tc.status == 413 {
			code = "body_too_large"
		}
		assertProblem(t, w, tc.status, code)
	}
	for _, key := range []string{"", "bad key", strings.Repeat("k", 129)} {
		r := accountRequest("POST", "/api/v1/users", `{"name":"alice","password":"secret"}`, "a")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		assertProblem(t, f.serve(r), 400, "invalid_request")
	}
	r := accountRequest("POST", "/api/v1/users", `{"name":"alice","password":"secret"}`, "a")
	r.Header.Add("Idempotency-Key", "first")
	r.Header.Add("Idempotency-Key", "second")
	assertProblem(t, f.serve(r), 400, "invalid_request")
	r = accountRequest("POST", "/api/v1/auth/login", `{}`, "")
	r.Header.Set("Content-Type", "text/plain")
	assertProblem(t, f.serve(r), 415, "unsupported_media_type")
	if f.passwords.hashCalls != 0 || f.passwords.verifyCalls != 0 || f.passwords.dummyCalls != 0 {
		t.Fatal("invalid body/key reached password work")
	}
}

func TestAccountHTTPCRUDActorPaginationAndIdempotentReplay(t *testing.T) {
	createCalls, updateCalls, getCalls, listCalls, deleteCalls := 0, 0, 0, 0, 0
	repo := httpAccountRepository{
		create: func(_ context.Context, actor domain.Actor, input domain.UserInput, key string) (domain.User, bool, error) {
			assertAccountActor(t, actor)
			createCalls++
			if key != "create-once" || input.Name != "alice" || input.Locale != "zh-CN" || input.PasswordHash != "server-generated-phc" {
				t.Fatal("create contract did not apply server hash/default locale/key")
			}
			return domain.User{ID: itemID, Name: input.Name}, createCalls == 2, nil
		},
		update: func(_ context.Context, actor domain.Actor, id string, input domain.UserInput) (domain.User, error) {
			assertAccountActor(t, actor)
			updateCalls++
			if id != itemID || input.PasswordHash != "" || input.Locale != "ja-JP" || !input.Disabled {
				t.Fatal("update mapping changed")
			}
			return domain.User{ID: id, Name: input.Name, Disabled: input.Disabled}, nil
		},
		get: func(_ context.Context, actor domain.Actor, id string) (domain.User, error) {
			assertAccountActor(t, actor)
			getCalls++
			if id != userID {
				t.Fatal("/me trusted a request identity")
			}
			return domain.User{ID: id}, nil
		},
		list: func(_ context.Context, actor domain.Actor, cursor string, limit int, deleted bool) ([]domain.User, error) {
			assertAccountActor(t, actor)
			listCalls++
			if cursor != userID || limit != 1 || !deleted {
				t.Fatal("pagination query mapping changed")
			}
			return []domain.User{{ID: itemID, Name: "result"}}, nil
		},
		delete: func(_ context.Context, actor domain.Actor, id string) error {
			assertAccountActor(t, actor)
			deleteCalls++
			if id != itemID {
				t.Fatal("delete target changed")
			}
			return nil
		},
	}
	f := newAccountHTTPFixture(t, repo, nil)
	for i, want := range []int{201, 200} {
		r := accountRequest("POST", "/api/v1/users", `{"name":"alice","password":"secret"}`, "a")
		r.Header.Set("Idempotency-Key", "create-once")
		r.Header.Set("X-User-ID", itemID)
		r.Header.Set("X-Session-ID", itemID)
		r.Header.Set("Forwarded", "for=192.0.2.1")
		w := f.serve(r)
		if w.Code != want || strings.Contains(w.Body.String(), "server-generated-phc") {
			t.Fatalf("create/replay status=%d body=%s", w.Code, w.Body.String())
		}
		if (w.Header().Get("Idempotency-Replayed") == "true") != (i == 1) {
			t.Fatal("replay header incorrect")
		}
	}
	w := f.serve(accountRequest("PUT", "/api/v1/users/"+itemID, `{"name":"alice","displayName":"Alice","locale":"ja-JP","disabled":true}`, "a"))
	if w.Code != 200 {
		t.Fatalf("update: %s", w.Body.String())
	}
	w = f.serve(accountRequest("GET", "/api/v1/users/me", "", "u"))
	if w.Code != 200 {
		t.Fatalf("get me: %s", w.Body.String())
	}
	w = f.serve(accountRequest("GET", "/api/v1/users?cursor="+userID+"&limit=1&includeDeleted=true", "", "a"))
	var result struct {
		Data struct {
			Users      []domain.User `json:"users"`
			Pagination struct {
				NextCursor string `json:"nextCursor"`
				Limit      int    `json:"limit"`
			} `json:"pagination"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Data.Users) != 1 || result.Data.Pagination.NextCursor != itemID || result.Data.Pagination.Limit != 1 {
		t.Fatalf("users envelope: %s", w.Body.String())
	}
	w = f.serve(accountRequest("DELETE", "/api/v1/users/"+itemID, "", "a"))
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal("delete must return an empty 204")
	}
	if createCalls != 2 || updateCalls != 1 || getCalls != 1 || listCalls != 1 || deleteCalls != 1 {
		t.Fatal("CRUD request did not reach expected repository operation")
	}
}

func TestAccountHTTPEmptyActionsAndACLReplacementAreExplicit(t *testing.T) {
	revokes, grants := 0, 0
	f := newAccountHTTPFixture(t, httpAccountRepository{
		revoke: func(_ context.Context, actor domain.Actor, id, sid string) error {
			assertAccountActor(t, actor)
			revokes++
			if id != userID || sid != sessionID {
				t.Fatal("logout did not revoke actor's own session")
			}
			return nil
		},
		libraries: func(_ context.Context, actor domain.Actor, id string, ids []string) error {
			assertAccountActor(t, actor)
			grants++
			if id != itemID || ids == nil || len(ids) != 0 {
				t.Fatal("explicit empty ACL was not preserved")
			}
			return nil
		},
	}, nil)
	for _, body := range []string{"", `null`, `[]`, `{"sessionID":"forged"}`} {
		assertProblem(t, f.serve(accountRequest("POST", "/api/v1/auth/logout", body, "u")), 400, "invalid_request")
	}
	w := f.serve(accountRequest("POST", "/api/v1/auth/logout", `{}`, "u"))
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal("empty action object should return empty 204")
	}
	for _, path := range []string{"/api/v1/users/" + itemID, "/api/v1/users/" + itemID + "/sessions", "/api/v1/users/" + itemID + "/sessions/" + sessionID} {
		for _, body := range []string{`{}`, " ", `{"ignored":true}`} {
			assertProblem(t, f.serve(accountRequest("DELETE", path, body, "a")), 400, "invalid_request")
		}
	}
	for _, body := range []string{`{}`, `{"libraryIds":null}`, `{"libraryIds":["` + libraryID + `","` + libraryID + `"]}`, `{"libraryIds":["invalid"]}`} {
		assertProblem(t, f.serve(accountRequest("PUT", "/api/v1/users/"+itemID+"/libraries", body, "a")), 400, "invalid_request")
	}
	w = f.serve(accountRequest("PUT", "/api/v1/users/"+itemID+"/libraries", `{"libraryIds":[]}`, "a"))
	if w.Code != 204 || revokes != 1 || grants != 1 {
		t.Fatal("action or explicit ACL replacement mapping failed")
	}
}

func TestAccountHTTPRepositoryFailuresMapWithoutSuccessOrLeaks(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrForbidden, 403, "forbidden"}, {domain.ErrUnauthenticated, 401, "authentication_required"},
		{domain.ErrConflict, 409, "conflict"}, {domain.ErrLastAdmin, 409, "last_admin"},
		{domain.ErrSessionLimit, 429, "session_limit"}, {domain.ErrDatabase, 503, "not_ready"},
		{domain.ErrNotFound, 404, "not_found"}, {context.Canceled, 408, "request_timeout"},
		{context.DeadlineExceeded, 408, "request_timeout"}, {errors.New("unexpected storage failure"), 500, "internal_error"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			calls := 0
			f := newAccountHTTPFixture(t, httpAccountRepository{delete: func(_ context.Context, actor domain.Actor, id string) error {
				assertAccountActor(t, actor)
				calls++
				return fmt.Errorf("private rollback details: %w", tc.err)
			}}, nil)
			w := f.serve(accountRequest("DELETE", "/api/v1/users/"+itemID, "", "a"))
			assertProblem(t, w, tc.status, tc.code)
			if calls != 1 || strings.Contains(w.Body.String()+f.logs.String(), "private rollback") || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatal("failed transaction was reported as success or exposed storage details")
			}
		})
	}
}

func TestAccountHTTPFeatureDisabledAndServiceRequired(t *testing.T) {
	f := newAccountHTTPFixture(t, httpAccountRepository{}, func(c *config.Config) { c.EnableAccounts = false })
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/auth/login"}, {"POST", "/api/v1/auth/logout"}, {"GET", "/api/v1/users"},
		{"POST", "/api/v1/users"}, {"GET", "/api/v1/users/me"}, {"PUT", "/api/v1/users/me/profile"},
		{"DELETE", "/api/v1/users/" + itemID}, {"PUT", "/api/v1/users/" + itemID + "/libraries"},
	} {
		assertProblem(t, f.serve(accountRequest(route.method, route.path, `{}`, "a")), 404, "not_found")
	}
	if f.backend.authCalls != 0 || f.passwords.hashCalls != 0 {
		t.Fatal("disabled routes performed account work")
	}
	cfg := validConfig()
	cfg.EnableAccounts = true
	cfg.Accounts = config.DefaultAccountsConfig()
	if _, err := New(cfg, &fakeBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.Default()); err == nil {
		t.Fatal("enabled accounts accepted a missing service")
	}
}

func TestAccountHTTPInvalidQueryAndInputIdentityCannotReachRepository(t *testing.T) {
	f := newAccountHTTPFixture(t, httpAccountRepository{}, nil)
	for _, query := range []string{"limit=0", "limit=101", "limit=1&limit=2", "cursor=invalid", "includeDeleted=1", "includeDeleted=true&includeDeleted=false", "unknown=true", "%zz=1"} {
		assertProblem(t, f.serve(accountRequest("GET", "/api/v1/users?"+query, "", "a")), 400, "invalid_request")
	}
	for _, body := range []string{`{"displayName":"Alice","locale":"en-US","userId":"` + itemID + `"}`, `{"displayName":"Alice","locale":"en-US","admin":true}`, `{"displayName":"Alice","locale":"en-US","sessionId":"` + itemID + `"}`} {
		assertProblem(t, f.serve(accountRequest("PUT", "/api/v1/users/me/profile", body, "u")), 400, "invalid_request")
	}
	assertProblem(t, f.serve(accountRequest("POST", "/api/v1/auth/login?token=forged", `{}`, "")), 400, "invalid_request")
}

func TestAccountHTTPStoredLocaleOverridesRequestErrorLanguage(t *testing.T) {
	calls := 0
	f := newAccountHTTPFixture(t, httpAccountRepository{get: func(_ context.Context, actor domain.Actor, id string) (domain.User, error) {
		assertAccountActor(t, actor)
		calls++
		return domain.User{}, domain.ErrForbidden
	}}, nil)
	f.backend.auth = func(context.Context, string) (access.Principal, error) {
		return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientWeb, Locale: "zh-TW"}, nil
	}
	r := accountRequest("GET", "/api/v1/users/"+itemID, "", "u")
	r.Header.Set("Accept-Language", "ja-JP")
	w := f.serve(r)
	assertProblem(t, w, 403, "forbidden")
	if calls != 1 || w.Header().Get("Content-Language") != "zh-TW" || !strings.Contains(w.Body.String(), i18n.Message("forbidden", "zh-TW", "")) {
		t.Fatal("saved locale did not take precedence")
	}
	if r.Header.Get("Accept-Language") != "ja-JP" {
		t.Fatal("authentication mutated the caller's request headers")
	}
}

func TestAccountHTTPRequestCancellationStopsLoginBeforeToken(t *testing.T) {
	lookups, commits := 0, 0
	f := newAccountHTTPFixture(t, httpAccountRepository{
		credentials: func(context.Context, string) (domain.Credentials, error) {
			lookups++
			return domain.Credentials{UserID: userID, PasswordHash: "stored-phc"}, nil
		},
		login: func(context.Context, domain.LoginInput) (domain.SessionGrant, error) {
			commits++
			return domain.SessionGrant{Token: "should-not-exist"}, nil
		},
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := accountRequest("POST", "/api/v1/auth/login", `{"name":"alice","password":"value"}`, "").WithContext(ctx)
	assertProblem(t, f.serve(r), 408, "request_timeout")
	if lookups != 0 || f.passwords.verifyCalls != 0 {
		t.Fatal("pre-cancelled request performed password work")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	f.passwords.verify = func(context.Context, string, string) (bool, error) { cancel(); return true, nil }
	r = accountRequest("POST", "/api/v1/auth/login", `{"name":"alice","password":"value"}`, "").WithContext(ctx)
	w := f.serve(r)
	assertProblem(t, w, 408, "request_timeout")
	if lookups != 1 || commits != 0 || strings.Contains(w.Body.String(), "should-not-exist") {
		t.Fatal("cancellation after KDF still issued a token")
	}
}

func TestAccountHTTPMissingOrNullPasswordIsRejectedBeforeWork(t *testing.T) {
	f := newAccountHTTPFixture(t, httpAccountRepository{}, nil)
	for _, body := range []string{`{"name":"alice"}`, `{"name":"alice","password":null}`} {
		assertProblem(t, f.serve(accountRequest("POST", "/api/v1/auth/login", body, "")), 400, "invalid_request")
	}
	for _, body := range []string{`{}`, `{"oldPassword":"old"}`, `{"newPassword":"new"}`, `{"oldPassword":null,"newPassword":"new"}`, `{"oldPassword":"old","newPassword":null}`} {
		assertProblem(t, f.serve(accountRequest("PUT", "/api/v1/users/me/password", body, "u")), 400, "invalid_request")
	}
	if f.passwords.hashCalls != 0 || f.passwords.verifyCalls != 0 || f.passwords.dummyCalls != 0 {
		t.Fatal("missing/null password reached password work")
	}
}

func TestAccountHTTPAdmissionBoundAndCancellationRelease(t *testing.T) {
	const slots = 8 // Default password concurrency 2, four request slots per worker.
	entered := make(chan struct{}, slots+1)
	release := make(chan struct{})
	var closeOnce sync.Once
	defer closeOnce.Do(func() { close(release) })
	var lookups atomic.Int32
	f := newAccountHTTPFixture(t, httpAccountRepository{
		credentials: func(ctx context.Context, _ string) (domain.Credentials, error) {
			lookups.Add(1)
			entered <- struct{}{}
			select {
			case <-ctx.Done():
				return domain.Credentials{}, ctx.Err()
			case <-release:
				return domain.Credentials{}, domain.ErrNotFound
			}
		},
		get: func(context.Context, domain.Actor, string) (domain.User, error) {
			return domain.User{}, domain.ErrNotFound
		},
	}, func(c *config.Config) { c.RequestTimeoutSeconds = 10 })
	type runningRequest struct {
		cancel context.CancelFunc
		done   chan *httptest.ResponseRecorder
	}
	running := make([]runningRequest, slots)
	for i := range running {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan *httptest.ResponseRecorder, 1)
		running[i] = runningRequest{cancel, done}
		defer cancel()
		r := accountRequest("POST", "/api/v1/auth/login", fmt.Sprintf(`{"name":"user-%d","password":"value"}`, i), "").WithContext(ctx)
		go func() { done <- f.serve(r) }()
	}
	for i := 0; i < slots; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("admitted request did not reach credential lookup")
		}
	}
	for _, r := range []*http.Request{
		accountRequest("POST", "/api/v1/auth/login", `{"name":"overflow","password":"value"}`, ""),
		accountRequest("GET", "/api/v1/users/me", "", "u"),
	} {
		w := f.serve(r)
		assertProblem(t, w, 503, "account_busy")
		if w.Header().Get("Retry-After") != "1" {
			t.Fatal("busy response lacks bounded retry hint")
		}
	}
	if lookups.Load() != slots || f.backend.authCalls != 0 {
		t.Fatal("overflow queued or passed shared admission gate")
	}
	running[0].cancel()
	select {
	case w := <-running[0].done:
		assertProblem(t, w, 408, "request_timeout")
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled request did not release its admission slot")
	}
	// A protected route can reuse a slot released by the public login route.
	assertProblem(t, f.serve(accountRequest("GET", "/api/v1/users/me", "", "u")), 404, "not_found")
	for _, request := range running[1:] {
		request.cancel()
	}
	for _, request := range running[1:] {
		select {
		case w := <-request.done:
			assertProblem(t, w, 408, "request_timeout")
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled requests retained admission slots")
		}
	}
	closeOnce.Do(func() { close(release) })
	assertProblem(t, f.serve(accountRequest("POST", "/api/v1/auth/login", `{"name":"after-cancel","password":"value"}`, "")), 401, "authentication_required")
	if lookups.Load() != slots+1 || f.passwords.dummyCalls != 1 {
		t.Fatal("capacity did not recover or cancelled requests performed KDF work")
	}
}

func TestAccountHTTPOpenAPIMatchesRoutesAndHasValidRequiredArrays(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := validConfig()
		cfg.EnableAccounts = enabled
		cfg.Accounts = config.DefaultAccountsConfig()
		f := newAccountHTTPFixture(t, httpAccountRepository{}, func(c *config.Config) { c.EnableAccounts = enabled })
		spec := Specification(cfg)
		paths := spec["paths"].(map[string]any)
		routerRoutes := map[string]bool{}
		if err := chi.Walk(f.handler.(chi.Router), func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if strings.HasPrefix(path, "/api/v1/auth/") || strings.HasPrefix(path, "/api/v1/users") {
				routerRoutes[strings.ToLower(method)+" "+path] = true
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		documentedRoutes := map[string]bool{}
		for path, methods := range paths {
			if !strings.HasPrefix(path, "/api/v1/auth/") && !strings.HasPrefix(path, "/api/v1/users") {
				continue
			}
			for method, raw := range methods.(map[string]any) {
				documentedRoutes[method+" "+path] = true
				op := raw.(map[string]any)
				if path == "/api/v1/auth/login" {
					if _, exists := op["security"]; exists {
						t.Fatal("public login documented as authenticated")
					}
				} else if security, ok := op["security"].([]any); !ok || len(security) != 1 {
					t.Fatal("protected route lacks bearer security")
				}
				if method == "delete" {
					if _, exists := op["requestBody"]; exists {
						t.Fatal("DELETE documented with a body")
					}
				}
			}
		}
		if !reflect.DeepEqual(routerRoutes, documentedRoutes) {
			t.Fatalf("documented account routes differ: router=%v spec=%v", routerRoutes, documentedRoutes)
		}
		if enabled && len(routerRoutes) != 18 || !enabled && len(routerRoutes) != 0 {
			t.Fatalf("unexpected rollout route count %d", len(routerRoutes))
		}
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		var serialized map[string]any
		if err := json.Unmarshal(data, &serialized); err != nil {
			t.Fatal(err)
		}
		schemas := serialized["components"].(map[string]any)["schemas"].(map[string]any)
		var inspect func(any)
		inspect = func(value any) {
			switch node := value.(type) {
			case map[string]any:
				if required, exists := node["required"]; exists {
					// OpenAPI parameter.required is boolean; JSON Schema.required is an array.
					if _, parameter := node["in"]; !parameter {
						if _, requestBody := node["content"]; !requestBody {
							if _, ok := required.([]any); !ok {
								t.Fatalf("schema required must be an array, got %T", required)
							}
						}
					}
				}
				if ref, ok := node["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/schemas/") {
					if _, exists := schemas[strings.TrimPrefix(ref, "#/components/schemas/")]; !exists {
						t.Fatalf("unresolved schema %s", ref)
					}
				}
				for _, child := range node {
					inspect(child)
				}
			case []any:
				for _, child := range node {
					inspect(child)
				}
			}
		}
		inspect(serialized)
		for _, name := range []string{"Login", "CreateUser", "UserSettings", "Profile", "PasswordChange", "LibraryAccess", "Empty", "Rotate"} {
			schema := schemas[name].(map[string]any)
			if schema["additionalProperties"] != false {
				t.Fatal("account input schema permits extra properties")
			}
			properties := schema["properties"].(map[string]any)
			for _, forbidden := range []string{"passwordHash", "clientKind", "userId", "sessionId"} {
				if _, exists := properties[forbidden]; exists {
					t.Fatal("client-controlled account input exposes internal privilege/identity field")
				}
			}
		}
	}
}
