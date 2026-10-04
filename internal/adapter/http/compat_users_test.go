package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/compat"
)

var compatLoginHeader = compat.FormatClientAuth(compat.ClientAuth{Client: "Test Player", Device: "Living Room", DeviceID: "device-1", Version: "1.2.3"})

func compatUserRequest(method, target, body string, header http.Header) *http.Request {
	r := httptest.NewRequest(method, "http://localhost"+target, strings.NewReader(body))
	r.RemoteAddr = "198.51.100.23:12345"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range header {
		r.Header[k] = vs
	}
	return r
}

func compatTokenAuth(token string) http.Header {
	return http.Header{"Authorization": {compat.FormatClientAuth(compat.ClientAuth{Client: "Test Player", DeviceID: "device-1", Token: token})}}
}

// Without the account service the user module is not mounted.
func TestCompatUserRoutesNeedAccounts(t *testing.T) {
	_, do := compatFixture(t, true)
	for _, target := range []string{"/compat/Users/Me", "/compat/Users/Public"} {
		if w := do(http.MethodGet, target, compatAuth(compatNativeToken)); w.Code != 404 || w.Body.Len() != 0 {
			t.Fatalf("%s: %d %q", target, w.Code, w.Body)
		}
	}
}

// TestCompatUsersPostgres drives the user module through the real account
// tables: login issues a native session only with the native permission,
// failures share the native route's lockout and audit, the session works on
// /Users/Me and the server's own API, and logout revokes exactly it.
func TestCompatUsersPostgres(t *testing.T) {
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
	serve := func(method, target, body string, header http.Header) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, compatUserRequest(method, target, body, header))
		return w
	}
	native := func(method, target, body, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := accountRequest(method, target, body, "")
		r.Header.Set("Authorization", "Bearer "+bearer)
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
	empty := func(name string, w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status || w.Body.Len() != 0 {
			t.Fatalf("%s: %d %q, want empty %d", name, w.Code, w.Body.String(), status)
		}
	}
	login := func(user, password string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"Username": user, "Pw": password})
		return serve(http.MethodPost, "/compat/Users/AuthenticateByName", string(body), http.Header{"Authorization": {compatLoginHeader}})
	}
	activeSessions := `SELECT count(*) FROM sessions WHERE user_id=$1::uuid AND revoked_at IS NULL`
	viewerWire := strings.ReplaceAll(f.viewer, "-", "")

	// Native permission off (the default): the verified password gets the
	// upstream-like refusal, audited, nothing issued. Wrong password and an
	// unknown account give one identical answer.
	empty("native not allowed", login("leak-viewer", "correct horse battery"), 403)
	if count(`SELECT count(*) FROM audit_logs WHERE event='login.native_denied' AND target_id=$1::uuid AND actor_ip='198.51.100.23'::inet`, f.viewer) != 1 ||
		count(activeSessions, f.viewer) != 0 {
		t.Fatal("refused compatibility login not audited or issued a session")
	}
	wrong, unknown := login("leak-viewer", "wrong"), login("no-such-account", "wrong")
	empty("wrong password", wrong, 401)
	empty("unknown account", unknown, 401)
	if len(wrong.Header()) != len(unknown.Header()) {
		t.Fatal("wrong password and unknown account are distinguishable")
	}

	// Allow native devices; the compatibility login now issues a native session.
	if w := native("PUT", "/api/v1/users/"+f.viewer+"/native", `{"allowNative":true}`, f.adminToken); w.Code != 200 {
		t.Fatalf("enable native: %d %s", w.Code, w.Body.String())
	}
	created := count(`SELECT count(*) FROM audit_logs WHERE event='session.created'`)
	w := login("leak-viewer", "correct horse battery")
	var result struct {
		User struct {
			ID     string `json:"Id"`
			Name   string `json:"Name"`
			Policy struct {
				IsAdministrator     bool `json:"IsAdministrator"`
				EnableMediaPlayback bool `json:"EnableMediaPlayback"`
			} `json:"Policy"`
		} `json:"User"`
		SessionInfo struct {
			ID       string `json:"Id"`
			UserID   string `json:"UserId"`
			Client   string `json:"Client"`
			DeviceID string `json:"DeviceId"`
		} `json:"SessionInfo"`
		AccessToken string `json:"AccessToken"`
		ServerID    string `json:"ServerId"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	if result.User.ID != viewerWire || result.SessionInfo.UserID != viewerWire || result.User.Name != "leak-viewer" || result.User.Policy.IsAdministrator ||
		!result.User.Policy.EnableMediaPlayback || result.SessionInfo.Client != "Test Player" || result.SessionInfo.DeviceID != "device-1" || len(result.ServerID) != 32 {
		t.Fatalf("login result: %s", w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "198.51.100.23") {
		t.Fatalf("login set a cookie or published the client address: %s", w.Body.String())
	}
	token := result.AccessToken
	if count(`SELECT count(*) FROM sessions WHERE replace(id::text,'-','')=$1 AND user_id=$2::uuid AND client_kind='native' AND revoked_at IS NULL`, result.SessionInfo.ID, f.viewer) != 1 ||
		count(`SELECT count(*) FROM audit_logs WHERE event='session.created'`) != created+1 {
		t.Fatal("compatibility login did not issue an audited native session")
	}
	// The server's own API lists it with the client labels and accepts it
	// for direct delivery.
	if w = native("GET", "/api/v1/users/"+f.viewer+"/sessions", "", f.adminToken); !strings.Contains(w.Body.String(), `"client":"Test Player"`) || !strings.Contains(w.Body.String(), `"deviceName":"Living Room"`) || !strings.Contains(w.Body.String(), `"version":"1.2.3"`) {
		t.Fatalf("session listing: %s", w.Body.String())
	}
	if w = native("GET", "/api/v1/sources/"+f.source[leakVisible]+"/stream", "", token); w.Code != 200 {
		t.Fatalf("direct play with compatibility session: %d", w.Code)
	}

	// /Users/Me and /Users/{id}: self or administrator only.
	if w = serve("GET", "/compat/Users/Me", "", compatTokenAuth(token)); w.Code != 200 || !strings.Contains(w.Body.String(), `"Id":"`+viewerWire+`"`) {
		t.Fatalf("me: %d %s", w.Code, w.Body.String())
	}
	if w = serve("GET", "/compat/Users/"+f.viewer, "", compatTokenAuth(token)); w.Code != 200 || !strings.Contains(w.Body.String(), `"Name":"leak-viewer"`) {
		t.Fatalf("self by id: %d %s", w.Code, w.Body.String())
	}
	var adminID string
	if err := store.Pool.QueryRow(ctx, `SELECT replace(id::text,'-','') FROM users WHERE name='leak-admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	empty("other user", serve("GET", "/compat/Users/"+adminID, "", compatTokenAuth(token)), 403)
	empty("missing user", serve("GET", "/compat/Users/"+strings.Repeat("ab", 16), "", compatTokenAuth(token)), 403)
	if w = serve("GET", "/compat/Users/"+viewerWire, "", compatTokenAuth(f.adminToken)); w.Code != 200 || !strings.Contains(w.Body.String(), `"IsAdministrator":false`) {
		t.Fatalf("admin reads user: %d %s", w.Code, w.Body.String())
	}
	empty("admin missing user", serve("GET", "/compat/Users/"+strings.Repeat("ab", 16), "", compatTokenAuth(f.adminToken)), 404)
	if w = serve("GET", "/compat/Users/Public", "", nil); w.Code != 200 || w.Body.String() != "[]" {
		t.Fatalf("public users: %d %s", w.Code, w.Body.String())
	}

	// Logout revokes exactly this session; the old token is dead everywhere.
	second := login("leak-viewer", "correct horse battery")
	if second.Code != 200 {
		t.Fatalf("second login: %d", second.Code)
	}
	empty("logout", serve("POST", "/compat/Sessions/Logout", "", compatTokenAuth(token)), 204)
	empty("old token me", serve("GET", "/compat/Users/Me", "", compatTokenAuth(token)), 401)
	empty("old token logout", serve("POST", "/compat/Sessions/Logout", "", compatTokenAuth(token)), 401)
	assertProblem(t, native("GET", "/api/v1/users/me", "", token), 401, "authentication_required")
	if count(activeSessions, f.viewer) != 1 {
		t.Fatal("logout revoked more or less than the current session")
	}

	// Lockout and audit are shared with the native route: five failures here
	// lock both entry points.
	failed := count(`SELECT count(*) FROM audit_logs WHERE event='login.failed' AND target_id=$1::uuid`, f.viewer)
	for i := 0; i < 5; i++ {
		empty("failure", login("leak-viewer", "wrong"), 401)
	}
	empty("locked compat", login("leak-viewer", "correct horse battery"), 401)
	nativeLogin := `{"name":"leak-viewer","password":"correct horse battery","client":"Player","deviceId":"device-2"}`
	assertProblem(t, native("POST", "/api/v1/auth/login/native", nativeLogin, ""), 401, "authentication_required")
	if count(`SELECT count(*) FROM audit_logs WHERE event='login.failed' AND target_id=$1::uuid AND actor_ip='198.51.100.23'::inet`, f.viewer) != failed+5 {
		t.Fatal("compatibility login failures not audited like native ones")
	}
}

// The compatibility login draws from the same rate limit buckets as the
// server's own login routes, in both directions.
func TestCompatLoginSharesRateLimitPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	_ = leakFixture(t, ctx, store)
	cfg := leakConfig(t, dsn, 0)
	cfg.Accounts.LoginUserLimit = 3
	handler := leakHandler(t, store, cfg)
	compatLogin := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, compatUserRequest(http.MethodPost, "/compat/Users/AuthenticateByName", `{"Username":"limited-name","Pw":"x"}`, http.Header{"Authorization": {compatLoginHeader}}))
		return w
	}
	nativeLogin := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, accountRequest("POST", "/api/v1/auth/login/native", `{"name":"limited-name","password":"x","client":"c","deviceId":"d"}`, ""))
		return w
	}
	for i, w := range []*httptest.ResponseRecorder{compatLogin(), nativeLogin(), compatLogin()} {
		if w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	w := compatLogin()
	if w.Code != 429 || w.Body.Len() != 0 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("compat after shared budget: %d %q", w.Code, w.Body.String())
	}
	assertProblem(t, nativeLogin(), 429, "auth_rate_limited")
}
