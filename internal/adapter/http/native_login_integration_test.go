package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNativeLoginPostgres drives native password login through the real
// account tables: the per-user permission, the audit of its change, a
// playable native grant without cookie or CSRF, the unchanged web login, the
// browser refusal, shared lockout and the session listings.
func TestNativeLoginPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	f := leakFixture(t, ctx, store)
	if _, err := store.SetLocalPassword(ctx, "leak-viewer", "$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo"); err != nil {
		t.Fatal(err)
	}
	passwords := &httpAccountPasswords{verify: func(_ context.Context, password, _ string) (bool, error) {
		return password == "correct horse battery", nil
	}}
	cfg := leakConfig(t, dsn, 0)
	cfg.Accounts.LoginUserLimit = 100
	handler := leakHandlerWith(t, store, cfg, passwords)
	serve := func(method, path, body, bearer string, headers ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := accountRequest(method, path, body, "")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := store.Pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	type grant struct {
		Token   string          `json:"token"`
		CSRF    *string         `json:"csrf"`
		User    map[string]any  `json:"user"`
		Session json.RawMessage `json:"session"`
	}
	decode := func(w *httptest.ResponseRecorder) grant {
		t.Helper()
		var body struct {
			Data grant `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("grant: %d %s", w.Code, w.Body.String())
		}
		return body.Data
	}
	const login = `{"name":"leak-viewer","password":"correct horse battery","client":"Player","device":"Living room","deviceId":"device-1","version":"1.2.3"}`
	stream := "/api/v1/sources/" + f.source[leakVisible] + "/stream"

	// Default: password verified, native refused, nothing issued.
	w := serve("POST", "/api/v1/auth/login/native", login, "")
	assertProblem(t, w, 403, "native_login_disabled")
	if w.Header().Get("Set-Cookie") != "" || count(`SELECT count(*) FROM sessions WHERE user_id=$1::uuid AND revoked_at IS NULL`, f.viewer) != 0 {
		t.Fatal("refused native login issued a credential")
	}

	// Only an administrator may allow native devices; the change is audited.
	for _, body := range []string{`{}`, `{"allowNative":null}`, `{"allowNative":"true"}`, `{"allowNative":true,"admin":true}`} {
		assertProblem(t, serve("PUT", "/api/v1/users/"+f.viewer+"/native", body, f.adminToken), 400, "invalid_request")
	}
	if w = serve("PUT", "/api/v1/users/"+f.viewer+"/native", `{"allowNative":true}`, f.adminToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"allowNative":true`) {
		t.Fatalf("enable native: %d %s", w.Code, w.Body.String())
	}
	if count(`SELECT count(*) FROM audit_logs WHERE event='user.native_access_changed' AND target_id=$1::uuid AND actor_ip='198.51.100.23'::inet`, f.viewer) != 1 {
		t.Fatal("native access change not audited with actor address")
	}

	// Any browser marker is refused before password work.
	verifies := passwords.verifyCalls
	for _, header := range []string{"Origin", "Sec-Fetch-Site", "Sec-Fetch-Mode"} {
		assertProblem(t, serve("POST", "/api/v1/auth/login/native", login, "", header, "same-origin"), 403, "forbidden")
	}
	if passwords.verifyCalls != verifies {
		t.Fatal("browser request reached password verification")
	}

	// Native grant: body token only, no cookie, no CSRF, plays media.
	w = serve("POST", "/api/v1/auth/login/native", login, "")
	native := decode(w)
	if native.CSRF != nil || len(w.Result().Cookies()) != 0 || !strings.Contains(string(native.Session), `"clientKind":"native"`) ||
		!strings.Contains(string(native.Session), `"deviceId":"device-1"`) || native.User["allowNative"] != true {
		t.Fatalf("native grant: %s", w.Body.String())
	}
	if w = serve("GET", stream, "", native.Token); w.Code != 200 || w.Body.String() != "Jelee synthetic leak probe\n" {
		t.Fatalf("native direct play: %d %s", w.Code, w.Body.String())
	}

	// Web login is unchanged and still cannot play, by bearer or cookie.
	w = serve("POST", "/api/v1/auth/login", `{"name":"leak-viewer","password":"correct horse battery"}`, "")
	web := decode(w)
	if web.CSRF == nil || sessionCookie(t, w) == nil || !strings.Contains(string(web.Session), `"clientKind":"web"`) {
		t.Fatalf("web login changed: %s", w.Body.String())
	}
	assertProblem(t, serve("GET", stream, "", web.Token), 403, "web_playback_disabled")
	// A plain user cannot change the permission, not even its own.
	assertProblem(t, serve("PUT", "/api/v1/users/"+f.viewer+"/native", `{"allowNative":false}`, web.Token), 403, "forbidden")
	cookie := cookieRequest("GET", stream, "", web.Token, "")
	cw := httptest.NewRecorder()
	handler.ServeHTTP(cw, cookie)
	assertProblem(t, cw, 403, "web_playback_disabled")
	// A native token is still refused as a cookie.
	cookie = cookieRequest("GET", "/api/v1/users/me", "", native.Token, "")
	cw = httptest.NewRecorder()
	handler.ServeHTTP(cw, cookie)
	assertProblem(t, cw, 401, "authentication_required")

	// Listings: the user's own sessions and, for administrators, all of them.
	w = serve("GET", "/api/v1/users/"+f.viewer+"/sessions", "", native.Token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"client":"Player"`) || !strings.Contains(w.Body.String(), `"version":"1.2.3"`) ||
		!strings.Contains(w.Body.String(), `"deviceName":"Living room"`) || !strings.Contains(w.Body.String(), `"lastIp":"198.51.100.23"`) || !strings.Contains(w.Body.String(), `"lastSeenAt":`) {
		t.Fatalf("own sessions: %d %s", w.Code, w.Body.String())
	}
	assertProblem(t, serve("GET", "/api/v1/sessions", "", native.Token), 403, "forbidden")
	assertProblem(t, serve("GET", "/api/v1/sessions?limit=0", "", f.adminToken), 400, "invalid_request")
	assertProblem(t, serve("GET", "/api/v1/sessions?unknown=1", "", f.adminToken), 400, "invalid_request")
	w = serve("GET", "/api/v1/sessions?limit=100", "", f.adminToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"deviceId":"device-1"`) || !strings.Contains(w.Body.String(), `"clientKind":"web"`) {
		t.Fatalf("all sessions: %d %s", w.Code, w.Body.String())
	}

	// Native login shares lockout: five failures (the handler's LockAfter)
	// lock the account for both endpoints.
	for i := 0; i < 5; i++ {
		assertProblem(t, serve("POST", "/api/v1/auth/login/native", strings.Replace(login, "correct horse battery", "wrong", 1), ""), 401, "authentication_required")
	}
	assertProblem(t, serve("POST", "/api/v1/auth/login/native", login, ""), 401, "authentication_required")
	assertProblem(t, serve("POST", "/api/v1/auth/login", `{"name":"leak-viewer","password":"correct horse battery"}`, ""), 401, "authentication_required")
	if count(`SELECT count(*) FROM audit_logs WHERE event='login.failed' AND target_id=$1::uuid`, f.viewer) != 5 {
		t.Fatal("native login failures not audited")
	}
	if w = serve("POST", "/api/v1/users/"+f.viewer+"/unlock", `{}`, f.adminToken); w.Code != 204 {
		t.Fatalf("unlock: %d", w.Code)
	}

	// Withdrawing the permission ends playback at once.
	if w = serve("PUT", "/api/v1/users/"+f.viewer+"/native", `{"allowNative":false}`, f.adminToken); w.Code != 200 {
		t.Fatalf("disable native: %d %s", w.Code, w.Body.String())
	}
	assertProblem(t, serve("GET", stream, "", native.Token), 401, "authentication_required")
	assertProblem(t, serve("POST", "/api/v1/auth/login/native", login, ""), 403, "native_login_disabled")
	if w = serve("GET", "/api/v1/users/me", "", web.Token); w.Code != 200 {
		t.Fatal("withdrawal revoked the web session")
	}
}
