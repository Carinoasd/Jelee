package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func nativeLoginRepository(t *testing.T, commits *[]domain.LoginInput, result error) httpAccountRepository {
	t.Helper()
	return httpAccountRepository{
		credentials: func(context.Context, string) (domain.Credentials, error) {
			return domain.Credentials{UserID: userID, PasswordHash: "private-stored-phc", Version: 1}, nil
		},
		login: func(_ context.Context, input domain.LoginInput) (domain.SessionGrant, error) {
			*commits = append(*commits, input)
			if result != nil {
				return domain.SessionGrant{}, result
			}
			return domain.SessionGrant{Token: strings.Repeat("n", 43), User: domain.User{ID: userID}, Session: domain.Session{ID: sessionID, ClientKind: "native", Client: input.Client.Name}}, nil
		},
	}
}

const nativeLoginBody = `{"name":"alice","password":"private-password","client":"Player","device":"Living room","deviceId":"device-1","version":"1.2.3"}`

func TestNativeLoginIssuesBodyOnlyNativeGrant(t *testing.T) {
	var commits []domain.LoginInput
	f := newAccountHTTPFixture(t, nativeLoginRepository(t, &commits, nil), nil)
	w := f.serve(accountRequest("POST", "/api/v1/auth/login/native", nativeLoginBody, ""))
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
		t.Fatalf("native login: %d %s", w.Code, w.Body.String())
	}
	if _, ok := body.Data["csrf"]; ok {
		t.Fatal("native grant carries a CSRF token")
	}
	if len(w.Result().Cookies()) != 0 || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("native login set a cookie")
	}
	if len(commits) != 1 || !commits[0].Native || commits[0].IP != "198.51.100.23" || commits[0].DeviceName != "Living room" ||
		commits[0].Client != (domain.NativeClient{Name: "Player", Device: "Living room", DeviceID: "device-1", Version: "1.2.3"}) {
		t.Fatalf("native login input: %+v", commits)
	}
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(f.logs.String(), strings.Repeat("n", 43)) {
		t.Fatal("native credential cacheable or logged")
	}
}

func TestNativeLoginRefusesBrowserRequestsBeforeAnyWork(t *testing.T) {
	var commits []domain.LoginInput
	f := newAccountHTTPFixture(t, nativeLoginRepository(t, &commits, nil), nil)
	for _, header := range [][2]string{
		{"Origin", "http://localhost"}, {"Origin", "https://evil.example"}, {"Origin", "null"}, {"Origin", ""},
		{"Sec-Fetch-Site", "same-origin"}, {"Sec-Fetch-Mode", "cors"},
	} {
		r := accountRequest("POST", "/api/v1/auth/login/native", nativeLoginBody, "")
		r.Header[header[0]] = []string{header[1]}
		assertProblem(t, f.serve(r), 403, "forbidden")
	}
	if len(commits) != 0 || f.passwords.verifyCalls != 0 || f.passwords.dummyCalls != 0 {
		t.Fatal("browser request reached password work")
	}
	// The web login is unaffected by Origin: browsers are its only client.
	r := accountRequest("POST", "/api/v1/auth/login", `{"name":"alice","password":"private-password"}`, "")
	r.Header.Set("Origin", "http://localhost")
	if w := f.serve(r); w.Code != 200 {
		t.Fatalf("web login with Origin: %d %s", w.Code, w.Body.String())
	}
}

func TestNativeLoginDisabledMapsToStableCode(t *testing.T) {
	var commits []domain.LoginInput
	f := newAccountHTTPFixture(t, nativeLoginRepository(t, &commits, domain.ErrNativeLoginDisabled), nil)
	for _, locale := range []string{"en-US", "zh-CN", "zh-TW", "ja-JP"} {
		r := accountRequest("POST", "/api/v1/auth/login/native", nativeLoginBody, "")
		r.Header.Set("Accept-Language", locale)
		w := f.serve(r)
		assertProblem(t, w, 403, "native_login_disabled")
		if strings.Contains(w.Body.String(), "private") || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("refusal leaked input or set a cookie")
		}
	}
}

func TestNativeLoginValidatesClientFields(t *testing.T) {
	var commits []domain.LoginInput
	f := newAccountHTTPFixture(t, nativeLoginRepository(t, &commits, nil), func(c *config.Config) { c.Accounts.LoginIPLimit, c.Accounts.LoginUserLimit = 10000, 1000 })
	field := func(key, value string) string {
		fields := map[string]string{"name": "alice", "password": "private-password", "client": "Player", "deviceId": "device-1"}
		fields[key] = value
		data, _ := json.Marshal(fields)
		return string(data)
	}
	for _, body := range []string{
		`{"name":"alice","password":"private-password","deviceId":"device-1"}`,
		`{"name":"alice","password":"private-password","client":"Player"}`,
		`{"name":"alice","client":"Player","deviceId":"device-1"}`,
		field("client", ""), field("client", " Player"), field("client", strings.Repeat("c", 129)), field("client", "Pla\x00yer"),
		field("deviceId", ""), field("deviceId", "device-1 "), field("deviceId", strings.Repeat("d", 257)), field("deviceId", "dev\u0085ice"),
		field("device", strings.Repeat("e", 129)), field("device", "Living\nroom"),
		field("version", strings.Repeat("9", 65)), field("version", "1.0\t"),
		field("kind", "native"), field("clientKind", "native"), field("admin", "true"),
	} {
		assertProblem(t, f.serve(accountRequest("POST", "/api/v1/auth/login/native", body, "")), 400, "invalid_request")
	}
	if len(commits) != 0 || f.passwords.verifyCalls != 0 {
		t.Fatal("invalid native client fields reached password work")
	}
	for _, body := range []string{
		field("client", strings.Repeat("c", 128)), field("deviceId", strings.Repeat("d", 256)), field("device", strings.Repeat("e", 128)),
		field("version", strings.Repeat("9", 64)), field("client", "播放器"), field("device", ""), field("version", ""),
	} {
		if w := f.serve(accountRequest("POST", "/api/v1/auth/login/native", body, "")); w.Code != 200 {
			t.Fatalf("boundary native client fields refused: %d %s", w.Code, body)
		}
	}
}

func TestNativeLoginSharesLoginRateLimit(t *testing.T) {
	var commits []domain.LoginInput
	f := newAccountHTTPFixture(t, nativeLoginRepository(t, &commits, nil), func(c *config.Config) { c.Accounts.LoginUserLimit = 2 })
	paths := []string{"/api/v1/auth/login", "/api/v1/auth/login/native"}
	for i := 0; i < 2; i++ {
		body := `{"name":"alice","password":"private-password"}`
		if paths[i] != "/api/v1/auth/login" {
			body = nativeLoginBody
		}
		if w := f.serve(accountRequest("POST", paths[i], body, "")); w.Code != 200 {
			t.Fatalf("login %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w := f.serve(accountRequest("POST", "/api/v1/auth/login/native", nativeLoginBody, ""))
	assertProblem(t, w, 429, "auth_rate_limited")
	if w.Header().Get("Retry-After") == "" || len(commits) != 2 {
		t.Fatal("native login did not share the login budget")
	}
}
