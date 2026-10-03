package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
)

type pgCookieClient struct {
	t       *testing.T
	handler http.Handler
}

type pgCookieResponse struct {
	*httptest.ResponseRecorder
	cookie *http.Cookie
	grant  struct {
		Token   string `json:"token"`
		CSRF    string `json:"csrf"`
		Session struct {
			ID         string `json:"id"`
			UserID     string `json:"userId"`
			ClientKind string `json:"clientKind"`
		} `json:"session"`
	}
}

func (c pgCookieClient) do(method, path, body, cookie, csrf, bearer string) pgCookieResponse {
	c.t.Helper()
	r := cookieRequest(method, path, body, cookie, csrf)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	response := pgCookieResponse{ResponseRecorder: w, cookie: sessionCookie(c.t, w)}
	if w.Code == 200 && strings.HasPrefix(w.Body.String(), `{"data":`) {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &envelope) == nil {
			_ = json.Unmarshal(envelope.Data, &response.grant)
		}
	}
	return response
}

func (c pgCookieClient) login() pgCookieResponse {
	c.t.Helper()
	w := c.do("POST", "/api/v1/auth/login", `{"name":"cookie-user","password":"correct horse battery"}`, "", "", "")
	if w.Code != 200 || w.cookie == nil || w.cookie.Value != w.grant.Token || w.grant.Session.ClientKind != "web" || w.grant.CSRF != csrfToken(w.grant.Token) {
		c.t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	return w
}

// TestWebSessionCookiePostgres drives the cookie session through the real
// session table: issue, CSRF, rotation, logout and revocation take effect on
// the very next request because every request re-reads the session row.
func TestWebSessionCookiePostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	native, err := store.Provision(ctx, "cookie-user", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetLocalPassword(ctx, "cookie-user", "$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo"); err != nil {
		t.Fatal(err)
	}
	// Resetting the password revoked the provisioned session; issue a fresh one.
	if native, err = store.Provision(ctx, "cookie-native", access.ClientNative, false); err != nil {
		t.Fatal(err)
	}
	c := pgCookieClient{t: t, handler: leakHandler(t, store, leakConfig(t, dsn, 0))}

	first := c.login()
	token := first.grant.Token
	if w := c.do("GET", "/api/v1/users/me", "", token, "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "cookie-user") {
		t.Fatalf("cookie read: %d %s", w.Code, w.Body.String())
	}
	if w := c.do("GET", "/api/v1/items", "", token, "", ""); w.Code != 200 {
		t.Fatalf("cookie catalog: %d %s", w.Code, w.Body.String())
	}

	// A native credential is valid as bearer but refused through the cookie.
	if w := c.do("GET", "/api/v1/users/me", "", "", "", native); w.Code != 200 {
		t.Fatalf("native bearer: %d", w.Code)
	}
	w := c.do("GET", "/api/v1/users/me", "", native, "", "")
	assertProblem(t, w.ResponseRecorder, 401, "authentication_required")
	if w.cookie == nil || w.cookie.MaxAge >= 0 {
		t.Fatal("native cookie was not expired")
	}

	profile := `{"displayName":"Cookie","locale":"en-US","hidden":false}`
	for _, csrf := range []string{"", csrfToken(native), strings.Repeat("A", 43)} {
		assertProblem(t, c.do("PUT", "/api/v1/users/me/profile", profile, token, csrf, "").ResponseRecorder, 403, "csrf_failed")
	}
	var display string
	if err = store.Pool.QueryRow(ctx, `SELECT display_name FROM users WHERE name='cookie-user'`).Scan(&display); err != nil || display == "Cookie" {
		t.Fatalf("profile changed without CSRF: %q %v", display, err)
	}
	if w = c.do("PUT", "/api/v1/users/me/profile", profile, token, first.grant.CSRF, ""); w.Code != 200 {
		t.Fatalf("profile with CSRF: %d %s", w.Code, w.Body.String())
	}
	// The same web credential as bearer needs no CSRF token.
	if w = c.do("PUT", "/api/v1/users/me/profile", profile, "", "", token); w.Code != 200 {
		t.Fatalf("bearer profile: %d %s", w.Code, w.Body.String())
	}
	if w = c.do("GET", "/api/v1/auth/csrf", "", token, "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), first.grant.CSRF) {
		t.Fatalf("csrf endpoint: %d %s", w.Code, w.Body.String())
	}

	// Rotation moves the cookie and the CSRF token; the old ones die at once.
	rotated := c.do("POST", "/api/v1/auth/rotate", `{}`, token, first.grant.CSRF, "")
	if rotated.Code != 200 || rotated.cookie == nil || rotated.cookie.Value != rotated.grant.Token || rotated.grant.Token == token || rotated.grant.CSRF != csrfToken(rotated.grant.Token) || rotated.grant.Session.ClientKind != "web" {
		t.Fatalf("rotate: %d %s", rotated.Code, rotated.Body.String())
	}
	assertProblem(t, c.do("GET", "/api/v1/users/me", "", token, "", "").ResponseRecorder, 401, "authentication_required")
	assertProblem(t, c.do("PUT", "/api/v1/users/me/profile", profile, rotated.grant.Token, first.grant.CSRF, "").ResponseRecorder, 403, "csrf_failed")
	token = rotated.grant.Token

	// Logout expires the cookie and the session.
	w = c.do("POST", "/api/v1/auth/logout", `{}`, token, rotated.grant.CSRF, "")
	if w.Code != 204 || w.cookie == nil || w.cookie.MaxAge >= 0 {
		t.Fatalf("logout: %d %v", w.Code, w.Header().Values("Set-Cookie"))
	}
	assertProblem(t, c.do("GET", "/api/v1/users/me", "", token, "", "").ResponseRecorder, 401, "authentication_required")

	// Revoking a web session from another session stops its cookie immediately.
	second := c.login()
	other := c.login()
	if w = c.do("GET", "/api/v1/users/me", "", second.grant.Token, "", ""); w.Code != 200 {
		t.Fatalf("second cookie: %d", w.Code)
	}
	w = c.do("DELETE", "/api/v1/users/"+second.grant.Session.UserID+"/sessions/"+second.grant.Session.ID, "", other.grant.Token, other.grant.CSRF, "")
	if w.Code != 204 || w.cookie != nil {
		t.Fatalf("revoke other session: %d %v", w.Code, w.Header().Values("Set-Cookie"))
	}
	w = c.do("GET", "/api/v1/users/me", "", second.grant.Token, "", "")
	assertProblem(t, w.ResponseRecorder, 401, "authentication_required")
	if w.cookie == nil || w.cookie.MaxAge >= 0 {
		t.Fatal("revoked cookie was not expired")
	}
	// Revoking every session of the user ends the caller's own cookie too.
	w = c.do("DELETE", "/api/v1/users/"+other.grant.Session.UserID+"/sessions", "", other.grant.Token, other.grant.CSRF, "")
	if w.Code != 204 || w.cookie == nil || w.cookie.MaxAge >= 0 {
		t.Fatalf("revoke all: %d %v", w.Code, w.Header().Values("Set-Cookie"))
	}
	assertProblem(t, c.do("GET", "/api/v1/users/me", "", other.grant.Token, "", "").ResponseRecorder, 401, "authentication_required")
}
