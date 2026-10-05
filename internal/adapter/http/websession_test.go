package httpapi

import (
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
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

var (
	webToken    = strings.Repeat("w", 43)
	nativeToken = strings.Repeat("n", 43)
	nextToken   = strings.Repeat("r", 43)
)

// sessionOperations supplies the session calls the shared HTTP account fake
// leaves to its embedded interface.
type sessionOperations struct {
	app.AccountRepository
	rotate    func(context.Context, domain.Actor, string, time.Duration) (domain.SessionGrant, error)
	revokeAll func(context.Context, domain.Actor, string) error
}

func (f sessionOperations) RotateSession(ctx context.Context, a domain.Actor, device string, ttl time.Duration) (domain.SessionGrant, error) {
	return f.rotate(ctx, a, device, ttl)
}
func (f sessionOperations) RevokeSessions(ctx context.Context, a domain.Actor, id string) error {
	return f.revokeAll(ctx, a, id)
}

type cookieFixture struct {
	*accountHTTPFixture
	revoked map[string]bool
	grants  []domain.SessionGrant
}

func newCookieFixture(t *testing.T, kind string) *cookieFixture {
	t.Helper()
	f := &cookieFixture{revoked: map[string]bool{}}
	grant := func(token string) domain.SessionGrant {
		return domain.SessionGrant{User: domain.User{ID: userID, Name: "viewer"}, Session: domain.Session{ID: sessionID, UserID: userID, ClientKind: kind, ExpiresAt: time.Now().Add(time.Hour)}, Token: token}
	}
	repo := httpAccountRepository{
		AccountRepository: sessionOperations{
			rotate: func(context.Context, domain.Actor, string, time.Duration) (domain.SessionGrant, error) {
				f.revoked[webToken] = true
				return grant(nextToken), nil
			},
			revokeAll: func(context.Context, domain.Actor, string) error { f.revoked[webToken] = true; return nil },
		},
		credentials: func(context.Context, string) (domain.Credentials, error) {
			return domain.Credentials{UserID: userID, Name: "viewer", PasswordHash: "stored", Version: 1}, nil
		},
		login: func(context.Context, domain.LoginInput) (domain.SessionGrant, error) { return grant(webToken), nil },
		get: func(_ context.Context, a domain.Actor, _ string) (domain.User, error) {
			return domain.User{ID: a.UserID, Name: "viewer"}, nil
		},
		profile: func(_ context.Context, a domain.Actor, _ domain.ProfileInput) (domain.User, error) {
			return domain.User{ID: a.UserID, Name: "viewer"}, nil
		},
		revoke: func(context.Context, domain.Actor, string, string) error { f.revoked[webToken] = true; return nil },
	}
	f.accountHTTPFixture = newAccountHTTPFixture(t, repo, func(c *config.Config) {
		c.EnableCatalog, c.EnableDirect = true, true
	})
	return f
}

func cookieTestBackend(revoked map[string]bool) func(context.Context, string) (access.Principal, error) {
	return func(_ context.Context, token string) (access.Principal, error) {
		if revoked[token] {
			return access.Principal{}, domain.ErrUnauthenticated
		}
		switch token {
		case webToken, nextToken:
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientWeb}, nil
		case nativeToken:
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative}, nil
		}
		return access.Principal{}, domain.ErrUnauthenticated
	}
}

func cookieRequest(method, path, body, cookie, csrf string) *http.Request {
	r := accountRequest(method, path, body, "")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	}
	if csrf != "" {
		r.Header.Set(csrfHeaderName, csrf)
	}
	return r
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	var found *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			if found != nil {
				t.Fatal("response sets the session cookie twice")
			}
			found = c
		}
	}
	return found
}

func assertCleared(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	c := sessionCookie(t, w)
	if c == nil || c.MaxAge >= 0 || c.Value != "" || !c.Secure || !c.HttpOnly || c.Path != "/" {
		t.Fatalf("session cookie not cleared: %v", w.Header().Values("Set-Cookie"))
	}
}

func TestLoginSetsHostOnlyWebSessionCookieAndCSRF(t *testing.T) {
	f := newCookieFixture(t, "web")
	w := f.serve(accountRequest("POST", "/api/v1/auth/login", `{"name":"viewer","password":"secret"}`, ""))
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	raw := w.Header().Values("Set-Cookie")
	if len(raw) != 1 || !strings.HasPrefix(raw[0], sessionCookieName+"="+webToken+";") {
		t.Fatalf("Set-Cookie = %q", raw)
	}
	for _, attribute := range []string{"Path=/", "HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(raw[0], "; "+attribute) {
			t.Fatalf("cookie lacks %s: %q", attribute, raw[0])
		}
	}
	if strings.Contains(strings.ToLower(raw[0]), "domain=") {
		t.Fatalf("__Host- cookie must not carry Domain: %q", raw[0])
	}
	c := sessionCookie(t, w)
	if c.MaxAge < 3590 || c.MaxAge > 3600 {
		t.Fatalf("Max-Age %d does not follow the one hour session lifetime", c.MaxAge)
	}
	var body struct {
		Data struct {
			Token string `json:"token"`
			CSRF  string `json:"csrf"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Data.Token != webToken || body.Data.CSRF != csrfToken(webToken) || len(body.Data.CSRF) != 43 {
		t.Fatalf("login body: %s", w.Body.String())
	}
	if csrfToken(webToken) == csrfToken(nextToken) {
		t.Fatal("CSRF token does not depend on the session")
	}
}

func TestNativeGrantNeverSetsCookie(t *testing.T) {
	f := newCookieFixture(t, "native")
	w := f.serve(accountRequest("POST", "/api/v1/auth/login", `{"name":"viewer","password":"secret"}`, ""))
	if w.Code != 200 || len(w.Header().Values("Set-Cookie")) != 0 || strings.Contains(w.Body.String(), `"csrf"`) {
		t.Fatalf("native grant: %d %v %s", w.Code, w.Header().Values("Set-Cookie"), w.Body.String())
	}
}

func TestCookieAuthenticationIsWebOnlyAndBearerUnchanged(t *testing.T) {
	f := newCookieFixture(t, "web")
	f.backend.auth = cookieTestBackend(f.revoked)
	if w := f.serve(cookieRequest("GET", "/api/v1/users/me", "", webToken, "")); w.Code != 200 || len(w.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("web cookie read: %d %s", w.Code, w.Body.String())
	}
	w := f.serve(cookieRequest("GET", "/api/v1/users/me", "", nativeToken, ""))
	assertProblem(t, w, 401, "authentication_required")
	assertCleared(t, w)
	for _, bad := range []string{"short", strings.Repeat("!", 43), strings.Repeat("x", 43)} {
		w = f.serve(cookieRequest("GET", "/api/v1/users/me", "", bad, ""))
		assertProblem(t, w, 401, "authentication_required")
		assertCleared(t, w)
	}
	r := cookieRequest("GET", "/api/v1/users/me", "", webToken, "")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webToken})
	assertProblem(t, f.serve(r), 401, "authentication_required")

	// Bearer keeps its exact behavior: native works, no CSRF is needed, and a
	// present but invalid header is never rescued by a valid cookie.
	for _, token := range []string{nativeToken, webToken} {
		r = accountRequest("GET", "/api/v1/users/me", "", "")
		r.Header.Set("Authorization", "Bearer "+token)
		if w = f.serve(r); w.Code != 200 {
			t.Fatalf("bearer read: %d", w.Code)
		}
		r = accountRequest("PUT", "/api/v1/users/me/profile", `{"locale":"en-US"}`, "")
		r.Header.Set("Authorization", "Bearer "+token)
		if w = f.serve(r); w.Code != 200 {
			t.Fatalf("bearer write without CSRF: %d %s", w.Code, w.Body.String())
		}
	}
	for _, header := range []string{"", "Bearer short", "Basic " + webToken, "Bearer " + strings.Repeat("x", 43)} {
		r = cookieRequest("GET", "/api/v1/users/me", "", webToken, "")
		r.Header.Set("Authorization", header)
		w = f.serve(r)
		assertProblem(t, w, 401, "authentication_required")
		if len(w.Header().Values("Set-Cookie")) != 0 {
			t.Fatal("bearer failure touched the browser cookie")
		}
	}
}

func TestCookieUnsafeMethodsRequireCSRF(t *testing.T) {
	f := newCookieFixture(t, "web")
	f.backend.auth = cookieTestBackend(f.revoked)
	good := csrfToken(webToken)
	forged := []string{"", "x", csrfToken(nextToken), csrfToken(nativeToken), strings.Repeat("A", 43), good[:42] + "_"}
	for _, value := range forged {
		w := f.serve(cookieRequest("PUT", "/api/v1/users/me/profile", `{"locale":"en-US"}`, webToken, value))
		assertProblem(t, w, 403, "csrf_failed")
	}
	r := cookieRequest("PUT", "/api/v1/users/me/profile", `{"locale":"en-US"}`, webToken, good)
	r.Header.Add(csrfHeaderName, good)
	assertProblem(t, f.serve(r), 403, "csrf_failed")
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/auth/logout"}, {"POST", "/api/v1/auth/rotate"}, {"PUT", "/api/v1/users/" + userID},
		{"DELETE", "/api/v1/users/" + userID + "/sessions"}, {"DELETE", "/api/v1/users/" + userID + "/sessions/" + sessionID},
	} {
		assertProblem(t, f.serve(cookieRequest(route.method, route.path, `{}`, webToken, "")), 403, "csrf_failed")
	}
	// No route uses PATCH yet; the middleware still guards it, and lets safe
	// methods through without a token.
	s := &Server{cfg: validConfig(), backend: f.backend}
	reached := 0
	guarded := s.authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ }))
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "CONNECT", "PROPFIND"} {
		w := httptest.NewRecorder()
		guarded.ServeHTTP(w, cookieRequest(method, "/x", "", webToken, ""))
		if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"csrf_failed"`) {
			t.Fatalf("%s without CSRF: %d %s", method, w.Code, w.Body.String())
		}
	}
	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		guarded.ServeHTTP(httptest.NewRecorder(), cookieRequest(method, "/x", "", webToken, ""))
	}
	if reached != 3 {
		t.Fatalf("middleware reached handler %d times, want only the 3 safe methods", reached)
	}
	if f.revoked[webToken] {
		t.Fatal("request without CSRF reached the repository")
	}
	r = cookieRequest("PUT", "/api/v1/users/me/profile", `{"locale":"en-US"}`, webToken, good)
	r.Header.Set("Accept-Language", "zh-TW")
	w := f.serve(r)
	if w.Code != 200 {
		t.Fatalf("profile with CSRF: %d %s", w.Code, w.Body.String())
	}
	w = f.serve(cookieRequest("GET", "/api/v1/auth/csrf", "", webToken, ""))
	var body struct {
		Data struct {
			CSRF string `json:"csrf"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Data.CSRF != good {
		t.Fatalf("csrf endpoint: %d %s", w.Code, w.Body.String())
	}
	r = cookieRequest("PUT", "/api/v1/users/me/profile", `{"locale":"en-US"}`, webToken, "")
	r.Header.Set("Accept-Language", "zh-TW")
	w = f.serve(r)
	assertProblem(t, w, 403, "csrf_failed")
	if !strings.Contains(w.Body.String(), "安全權杖") {
		t.Fatalf("csrf failure is not localized: %s", w.Body.String())
	}
}

func TestCookieLogoutRotateAndRevokeUpdateCookie(t *testing.T) {
	f := newCookieFixture(t, "web")
	f.backend.auth = cookieTestBackend(f.revoked)
	w := f.serve(cookieRequest("POST", "/api/v1/auth/rotate", `{}`, webToken, csrfToken(webToken)))
	c := sessionCookie(t, w)
	if w.Code != 200 || c == nil || c.Value != nextToken || !strings.Contains(w.Body.String(), csrfToken(nextToken)) {
		t.Fatalf("rotate: %d %v %s", w.Code, w.Header().Values("Set-Cookie"), w.Body.String())
	}
	w = f.serve(cookieRequest("GET", "/api/v1/users/me", "", webToken, ""))
	assertProblem(t, w, 401, "authentication_required")
	assertCleared(t, w)
	if w = f.serve(cookieRequest("GET", "/api/v1/users/me", "", nextToken, "")); w.Code != 200 {
		t.Fatalf("rotated cookie: %d", w.Code)
	}

	for _, test := range []struct{ method, path string }{
		{"POST", "/api/v1/auth/logout"},
		{"DELETE", "/api/v1/users/" + userID + "/sessions"},
		{"DELETE", "/api/v1/users/" + userID + "/sessions/" + sessionID},
	} {
		delete(f.revoked, webToken)
		body := `{}`
		if test.method == "DELETE" {
			body = ""
		}
		w = f.serve(cookieRequest(test.method, test.path, body, webToken, csrfToken(webToken)))
		if w.Code != 204 {
			t.Fatalf("%s %s: %d %s", test.method, test.path, w.Code, w.Body.String())
		}
		assertCleared(t, w)
		w = f.serve(cookieRequest("GET", "/api/v1/users/me", "", webToken, ""))
		assertProblem(t, w, 401, "authentication_required")
	}
	// A bearer logout of some other session leaves the browser cookie alone.
	delete(f.revoked, webToken)
	r := cookieRequest("POST", "/api/v1/auth/logout", `{}`, webToken, "")
	r.Header.Set("Authorization", "Bearer "+nativeToken)
	if w = f.serve(r); w.Code != 204 || len(w.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("foreign bearer logout: %d %v", w.Code, w.Header().Values("Set-Cookie"))
	}
}

func TestCookieWebSessionCannotPlay(t *testing.T) {
	f := newFixture(t, true, true)
	r := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/sources/"+sourceID+"/stream", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webToken})
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	assertProblem(t, w, 403, "web_playback_disabled")
	r = httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/sources/"+sourceID+"/stream", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: nativeToken})
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	assertProblem(t, w, 401, "authentication_required")
	if f.resolver.calls != 0 {
		t.Fatal("cookie request reached the media resolver")
	}
}
