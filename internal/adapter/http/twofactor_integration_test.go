package httpapi

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TestTwoFactorPostgres drives the optional second factor (G07.8) through the
// real account tables and HTTP: enrollment with a fake clock, the web login
// challenge, replay and skew, single-use challenges and recovery codes, the
// shared lock, native and compatibility logins refused with the password and
// accepted with an application password, revocation, disabling and the
// administrator reset.
func TestTwoFactorPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	f := leakFixture(t, ctx, store)
	for _, name := range []string{"leak-viewer", "leak-admin"} {
		if _, err := store.SetLocalPassword(ctx, name, "$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo"); err != nil {
			t.Fatal(err)
		}
	}
	passwords := &httpAccountPasswords{verify: func(_ context.Context, password, _ string) (bool, error) {
		return password == "correct horse battery", nil
	}}
	var mu sync.Mutex
	now := time.Unix(1800000000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	cfg := leakConfig(t, dsn, 0)
	cfg.Accounts.LoginUserLimit = 100
	progress, err := app.NewProgress(store, store, app.ProgressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := leakHandlerWithAccounts(t, store, cfg, passwords, progress, leakRenderer{},
		app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute, Box: testWebhookBox(t), Now: clock})
	serve := func(method, path, body, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := accountRequest(method, path, body, "")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	compatLogin := func(password string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"Username": "leak-viewer", "Pw": password})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, compatUserRequest(http.MethodPost, "/compat/Users/AuthenticateByName", string(body), http.Header{"Authorization": {compatLoginHeader}}))
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
	data := func(w *httptest.ResponseRecorder, status int, v any) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &struct {
			Data any `json:"data"`
		}{Data: v}); err != nil {
			t.Fatal(err)
		}
	}
	type grant struct {
		Token    string  `json:"token"`
		CSRF     *string `json:"csrf"`
		Required bool    `json:"secondFactorRequired"`
		Session  struct {
			ClientKind string `json:"clientKind"`
		} `json:"session"`
		Challenge string `json:"challenge"`
	}
	webLogin := func(name string) grant {
		t.Helper()
		var g grant
		data(serve("POST", "/api/v1/auth/login", `{"name":"`+name+`","password":"correct horse battery"}`, ""), 200, &g)
		return g
	}
	const nativeBody = `{"name":"leak-viewer","password":"%s","client":"Player","deviceId":"device-9"}`
	nativeLogin := func(password string) *httptest.ResponseRecorder {
		return serve("POST", "/api/v1/auth/login/native", strings.Replace(nativeBody, "%s", password, 1), "")
	}
	// Setting the passwords revoked the provisioned sessions: the
	// administrator signs in on the web; a provisioned native administrator
	// checks the web-only reset.
	adminToken := webLogin("leak-admin").Token
	nativeAdmin, err := store.Provision(ctx, "tf-native-admin", access.ClientNative, true)
	if err != nil {
		t.Fatal(err)
	}
	if w := serve("PUT", "/api/v1/users/"+f.viewer+"/native", `{"allowNative":true}`, adminToken); w.Code != 200 {
		t.Fatalf("allow native: %d", w.Code)
	}

	// Before enrollment nothing changes for any login path.
	web := webLogin("leak-viewer")
	if web.Required || web.Token == "" || web.CSRF == nil {
		t.Fatalf("plain web login: %+v", web)
	}
	var preNative grant
	data(nativeLogin("correct horse battery"), 200, &preNative)
	var status domain.TwoFactorStatus
	data(serve("GET", "/api/v1/users/"+f.viewer+"/two-factor", "", web.Token), 200, &status)
	if !status.Available || status.Enabled {
		t.Fatalf("status: %+v", status)
	}

	// Enrollment needs a web session.
	assertProblem(t, serve("POST", "/api/v1/users/me/two-factor/enroll", `{}`, preNative.Token), 403, "forbidden")
	var enrollment domain.TwoFactorEnrollment
	data(serve("POST", "/api/v1/users/me/two-factor/enroll", `{}`, web.Token), 200, &enrollment)
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	if err != nil || len(key) != 20 || !strings.HasPrefix(enrollment.URI, "otpauth://totp/Jelee:leak-viewer?secret=") {
		t.Fatalf("enrollment %+v", enrollment)
	}
	if count(`SELECT count(*) FROM user_totp WHERE position($2::bytea in secret_sealed)>0 AND user_id=$1::uuid`, f.viewer, key) != 0 {
		t.Fatal("secret stored in plain text")
	}
	code := func(offset int64) string {
		return domain.TOTPCode(key, domain.TOTPStep(clock())+offset)
	}
	wrong := func() string {
		for _, candidate := range []string{"000000", "111111", "222222"} {
			if candidate != code(-1) && candidate != code(0) && candidate != code(1) {
				return candidate
			}
		}
		return "333333"
	}
	assertProblem(t, serve("POST", "/api/v1/users/me/two-factor/confirm", `{"code":"`+wrong()+`"}`, web.Token), 400, "invalid_two_factor_code")
	var recovery domain.RecoveryCodes
	data(serve("POST", "/api/v1/users/me/two-factor/confirm", `{"code":"`+code(0)+`"}`, web.Token), 200, &recovery)
	if len(recovery.Codes) != 10 {
		t.Fatalf("recovery codes: %+v", recovery)
	}
	// The viewer's provisioned native session was established with the
	// password alone and is revoked; this web session stays.
	assertProblem(t, serve("GET", "/api/v1/users/me", "", preNative.Token), 401, "authentication_required")
	if w := serve("GET", "/api/v1/users/me", "", web.Token); w.Code != 200 {
		t.Fatal("enabling revoked the enrolling session")
	}

	// Third-party login paths refuse the account password with a clear code.
	assertProblem(t, nativeLogin("correct horse battery"), 403, "app_password_required")
	w := compatLogin("correct horse battery")
	if w.Code != 403 || w.Header().Get("X-Jelee-Error") != "app_password_required" || !strings.Contains(w.Body.String(), "application password") {
		t.Fatalf("compatibility password login: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	// A wrong password still looks like any wrong password.
	if w = compatLogin("wrong"); w.Code != 401 || w.Body.Len() != 0 {
		t.Fatalf("compatibility wrong password: %d", w.Code)
	}
	assertProblem(t, nativeLogin("wrong"), 401, "authentication_required")
	if w = serve("POST", "/api/v1/users/"+f.viewer+"/unlock", `{}`, adminToken); w.Code != 204 {
		t.Fatal("unlock")
	}

	// Web login: the password yields a challenge and no cookie.
	r := accountRequest("POST", "/api/v1/auth/login", `{"name":"leak-viewer","password":"correct horse battery"}`, "")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var challenge grant
	data(w, 200, &challenge)
	if !challenge.Required || len(challenge.Challenge) != 43 || challenge.Token != "" || sessionCookie(t, w) != nil {
		t.Fatalf("challenge: %s", w.Body.String())
	}
	step := func(challenge, field, value string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"challenge": challenge, field: value})
		return serve("POST", "/api/v1/auth/login/second-factor", string(body), "")
	}
	// The confirmation step cannot be replayed; skew of one step is accepted
	// forward; after that the older in-window step is refused.
	assertProblem(t, step(challenge.Challenge, "code", code(0)), 400, "invalid_two_factor_code")
	w = step(challenge.Challenge, "code", code(1))
	var completed grant
	data(w, 200, &completed)
	if completed.Session.ClientKind != "web" || completed.CSRF == nil || sessionCookie(t, w) == nil {
		t.Fatalf("completed login: %s", w.Body.String())
	}
	assertProblem(t, step(challenge.Challenge, "code", code(1)), 401, "login_challenge_invalid")
	second := webLogin("leak-viewer")
	assertProblem(t, step(second.Challenge, "code", code(0)), 400, "invalid_two_factor_code")
	advance(domain.TOTPPeriod)
	assertProblem(t, step(second.Challenge, "code", code(0)), 400, "invalid_two_factor_code") // == the step just used
	advance(domain.TOTPPeriod)
	if w = step(second.Challenge, "code", code(0)); w.Code != 200 {
		t.Fatalf("next step: %d %s", w.Code, w.Body.String())
	}
	if count(`SELECT count(*) FROM audit_logs WHERE event='login.second_factor_failed' AND target_id=$1::uuid`, f.viewer) != 3 {
		t.Fatal("failed second steps not audited")
	}
	// Exactly one of the two forms; malformed challenges are refused.
	assertProblem(t, serve("POST", "/api/v1/auth/login/second-factor", `{"challenge":"`+second.Challenge+`"}`, ""), 400, "invalid_request")
	assertProblem(t, serve("POST", "/api/v1/auth/login/second-factor", `{"challenge":"`+second.Challenge+`","code":"123456","recoveryCode":"x"}`, ""), 400, "invalid_request")
	assertProblem(t, step("short", "code", "123456"), 401, "login_challenge_invalid")

	// Recovery codes work once each.
	third := webLogin("leak-viewer")
	if w = step(third.Challenge, "recoveryCode", strings.ToUpper(recovery.Codes[0])); w.Code != 200 {
		t.Fatalf("recovery login: %d %s", w.Code, w.Body.String())
	}
	fourth := webLogin("leak-viewer")
	assertProblem(t, step(fourth.Challenge, "recoveryCode", recovery.Codes[0]), 400, "invalid_two_factor_code")

	// Codes and passwords share the lock: with one failure above, three
	// wrong codes and one wrong password reach LockAfter (5).
	for range 3 {
		assertProblem(t, step(fourth.Challenge, "code", wrong()), 400, "invalid_two_factor_code")
	}
	assertProblem(t, serve("POST", "/api/v1/auth/login", `{"name":"leak-viewer","password":"wrong"}`, ""), 401, "authentication_required")
	assertProblem(t, step(fourth.Challenge, "code", code(1)), 401, "authentication_required")
	assertProblem(t, serve("POST", "/api/v1/auth/login", `{"name":"leak-viewer","password":"correct horse battery"}`, ""), 401, "authentication_required")
	if w = serve("POST", "/api/v1/users/"+f.viewer+"/unlock", `{}`, adminToken); w.Code != 204 {
		t.Fatal("unlock")
	}

	// Application passwords: created from the web session, shown once,
	// accepted by native and compatibility logins.
	assertProblem(t, serve("POST", "/api/v1/users/me/app-passwords", `{"name":" TV"}`, web.Token), 400, "invalid_request")
	var created domain.NewAppPassword
	data(serve("POST", "/api/v1/users/me/app-passwords", `{"name":"Living room TV"}`, web.Token), 201, &created)
	if !domain.LooksLikeAppPassword(created.Password) || created.AppPassword.Name != "Living room TV" {
		t.Fatalf("created %+v", created)
	}
	var native grant
	data(nativeLogin(created.Password), 200, &native)
	if native.Session.ClientKind != "native" || native.CSRF != nil {
		t.Fatalf("native application password login: %+v", native)
	}
	if w = compatLogin(created.Password); w.Code != 200 || !strings.Contains(w.Body.String(), `"AccessToken"`) {
		t.Fatalf("compatibility application password login: %d %s", w.Code, w.Body.String())
	}
	var compatGrant struct {
		AccessToken string `json:"AccessToken"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &compatGrant)
	// It never replaces the second step of a web login.
	assertProblem(t, serve("POST", "/api/v1/auth/login", `{"name":"leak-viewer","password":"`+created.Password+`"}`, ""), 401, "authentication_required")
	// Sessions it issued cannot manage the factor or application passwords.
	assertProblem(t, serve("POST", "/api/v1/users/me/two-factor/disable", `{"password":"correct horse battery","recoveryCode":"`+recovery.Codes[1]+`"}`, native.Token), 403, "forbidden")
	assertProblem(t, serve("POST", "/api/v1/users/me/app-passwords", `{"name":"more"}`, native.Token), 403, "forbidden")
	assertProblem(t, serve("DELETE", "/api/v1/users/"+f.viewer+"/app-passwords/"+created.AppPassword.ID, "", native.Token), 403, "forbidden")
	var list []domain.AppPassword
	data(serve("GET", "/api/v1/users/"+f.viewer+"/app-passwords", "", web.Token), 200, &list)
	if len(list) != 1 || list[0].LastUsedAt == nil || strings.Contains(serve("GET", "/api/v1/users/"+f.viewer+"/app-passwords", "", web.Token).Body.String(), created.Password) {
		t.Fatalf("list: %+v", list)
	}
	if w = serve("DELETE", "/api/v1/users/"+f.viewer+"/app-passwords/"+created.AppPassword.ID, "", web.Token); w.Code != 204 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	assertProblem(t, serve("GET", "/api/v1/users/me", "", native.Token), 401, "authentication_required")
	assertProblem(t, serve("GET", "/api/v1/users/me", "", compatGrant.AccessToken), 401, "authentication_required")
	assertProblem(t, nativeLogin(created.Password), 401, "authentication_required")

	// Disabling needs the password and a factor.
	assertProblem(t, serve("POST", "/api/v1/users/me/two-factor/disable", `{"password":"wrong","recoveryCode":"`+recovery.Codes[1]+`"}`, web.Token), 400, "invalid_password")
	assertProblem(t, serve("POST", "/api/v1/users/me/two-factor/disable", `{"password":"correct horse battery","recoveryCode":"`+recovery.Codes[0]+`"}`, web.Token), 400, "invalid_two_factor_code")
	if w = serve("POST", "/api/v1/users/me/two-factor/disable", `{"password":"correct horse battery","recoveryCode":"`+recovery.Codes[1]+`"}`, web.Token); w.Code != 204 {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if w = nativeLogin("correct horse battery"); w.Code != 200 {
		t.Fatalf("native password login after disable: %d", w.Code)
	}

	// Administrator reset: needs an administrator's web session.
	data(serve("POST", "/api/v1/users/me/two-factor/enroll", `{}`, web.Token), 200, &enrollment)
	key, _ = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	advance(domain.TOTPPeriod)
	data(serve("POST", "/api/v1/users/me/two-factor/confirm", `{"code":"`+code(0)+`"}`, web.Token), 200, &recovery)
	assertProblem(t, serve("DELETE", "/api/v1/users/"+f.viewer+"/two-factor", "", web.Token), 403, "forbidden")
	assertProblem(t, serve("DELETE", "/api/v1/users/"+f.viewer+"/two-factor", "", nativeAdmin), 403, "forbidden")
	if w = serve("DELETE", "/api/v1/users/"+f.viewer+"/two-factor", "", adminToken); w.Code != 204 {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	data(serve("GET", "/api/v1/users/"+f.viewer+"/two-factor", "", adminToken), 200, &status)
	if status.Enabled || status.RecoveryCodesRemaining != 0 || count(`SELECT count(*) FROM audit_logs WHERE event='user.two_factor_reset' AND target_id=$1::uuid AND actor_id=(SELECT id FROM users WHERE name='leak-admin')`, f.viewer) != 1 {
		t.Fatalf("after reset: %+v", status)
	}
	if g := webLogin("leak-viewer"); g.Required {
		t.Fatal("reset account still challenged")
	}
}
