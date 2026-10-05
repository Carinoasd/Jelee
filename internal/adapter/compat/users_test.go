package compat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

const (
	testUserID    = "0f1e2d3c-4b5a-4968-8776-a5b4c3d2e1f0"
	testUserWire  = "0f1e2d3c4b5a49688776a5b4c3d2e1f0"
	testAdminID   = "11111111-2222-4333-8444-555555555555"
	testOtherID   = "99999999-8888-4777-8666-555555555555"
	testSessionID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	testClientIP  = "198.51.100.7"
)

// adminToken authenticates as testAdminID; nativeToken as testUserID.
var adminToken = tokC

var testCreated = time.Date(2026, 10, 4, 8, 30, 15, 123456789, time.FixedZone("CST", 8*3600))

type loginCall struct {
	name, password, ip string
	client             domain.NativeClient
}

type fakeAccounts struct {
	logins   []loginCall
	loginErr error
	gets     []string
	getErr   error
	deleted  bool
	revokes  [][2]string
	actors   []domain.Actor
}

func (f *fakeAccounts) LoginNative(_ context.Context, name, password string, client domain.NativeClient, ip string) (domain.SessionGrant, error) {
	f.logins = append(f.logins, loginCall{name, password, ip, client})
	if f.loginErr != nil {
		return domain.SessionGrant{}, f.loginErr
	}
	return domain.SessionGrant{
		User: domain.User{ID: testUserID, Name: name, DisplayName: "Viewer", Locale: "zh-TW", AllowNative: true, CreatedAt: testCreated},
		Session: domain.Session{ID: testSessionID, UserID: testUserID, ClientKind: "native", DeviceName: client.Device, Client: client.Name,
			DeviceID: client.DeviceID, Version: client.Version, CreatedAt: testCreated, ExpiresAt: testCreated.Add(24 * time.Hour)},
		Token: tokA,
	}, nil
}

// Get mimics the store: a plain user may read only itself.
func (f *fakeAccounts) Get(_ context.Context, actor domain.Actor, id string) (domain.User, error) {
	f.gets = append(f.gets, id)
	f.actors = append(f.actors, actor)
	if f.getErr != nil {
		return domain.User{}, f.getErr
	}
	if actor.UserID != testAdminID && id != actor.UserID {
		return domain.User{}, domain.ErrForbidden
	}
	user := domain.User{ID: id, Name: "viewer", DisplayName: "Viewer", Locale: "zh-TW", AllowNative: true, CreatedAt: testCreated}
	switch id {
	case testAdminID:
		user.Name, user.Admin = "admin", true
	case testOtherID:
		user.Name, user.Hidden, user.Disabled, user.AllowNative = "other", true, true, false
	}
	if f.deleted {
		user.DeletedAt = &testCreated
	}
	return user, nil
}

func (f *fakeAccounts) Revoke(_ context.Context, actor domain.Actor, userID, sessionID string) error {
	f.actors = append(f.actors, actor)
	f.revokes = append(f.revokes, [2]string{userID, sessionID})
	return nil
}

type userHarness struct {
	harness
	accounts  *fakeAccounts
	busy      bool
	admitted  int
	released  int
	limited   bool
	limitArgs [][2]string
}

func newUserHarness(t *testing.T) *userHarness {
	t.Helper()
	h := &userHarness{accounts: &fakeAccounts{}}
	handler, err := NewRouter(Options{
		Authenticate: func(_ context.Context, token string) (access.Principal, error) {
			h.authCalls++
			switch token {
			case nativeToken:
				return access.Principal{UserID: testUserID, SessionID: testSessionID, Kind: access.ClientNative}, nil
			case adminToken:
				return access.Principal{UserID: testAdminID, SessionID: testSessionID, Kind: access.ClientNative, Admin: true}, nil
			case webToken:
				return access.Principal{UserID: testUserID, SessionID: testSessionID, Kind: access.ClientWeb}, nil
			}
			return access.Principal{}, domain.ErrUnauthenticated
		},
		WriteRejection: func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusConflict) },
		ServerID:       testServerID,
		Timeout:        time.Second,
		Users: &UserOptions{
			Accounts: h.accounts,
			Admit: func() (func(), bool) {
				if h.busy {
					return nil, false
				}
				h.admitted++
				return func() { h.released++ }, true
			},
			AllowLogin: func(ip, name string) (bool, time.Duration) {
				h.limitArgs = append(h.limitArgs, [2]string{ip, name})
				return !h.limited, 2500 * time.Millisecond
			},
			ClientIP: func(*http.Request) string { return testClientIP },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.handler = handler
	return h
}

const loginHeader = `MediaBrowser Client="Test Player", Device="Living Room", DeviceId="device-1", Version="1.2.3"`

func (h *userHarness) login(header http.Header, contentType, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://localhost/compat/Users/AuthenticateByName", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	for k, vs := range header {
		r.Header[k] = vs
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func checkGolden(t *testing.T, name string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("%s: %d %s %s", name, w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	path := filepath.Join("testdata", "golden", name)
	if *updateGolden {
		if err := os.WriteFile(path, w.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("%s differs:\n got %s\nwant %s", path, w.Body, want)
	}
}

func assertEmpty(t *testing.T, name string, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Body.Len() != 0 {
		t.Fatalf("%s: %d %q, want empty %d", name, w.Code, w.Body, status)
	}
}

func assertGeneric(t *testing.T, name string, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Body.String() != genericError || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("%s: %d %q, want generic %d", name, w.Code, w.Body, status)
	}
}

func TestNewRouterRequiresCompleteUserOptions(t *testing.T) {
	full := UserOptions{Accounts: &fakeAccounts{}, Admit: func() (func(), bool) { return func() {}, true },
		AllowLogin: func(string, string) (bool, time.Duration) { return true, 0 }, ClientIP: func(*http.Request) string { return "" }}
	base := Options{Authenticate: func(context.Context, string) (access.Principal, error) { return access.Principal{}, nil },
		WriteRejection: func(http.ResponseWriter, *http.Request, error) {}, ServerID: testServerID, Timeout: time.Second}
	for name, change := range map[string]func(*UserOptions){
		"accounts": func(o *UserOptions) { o.Accounts = nil },
		"admit":    func(o *UserOptions) { o.Admit = nil },
		"limiter":  func(o *UserOptions) { o.AllowLogin = nil },
		"ip":       func(o *UserOptions) { o.ClientIP = nil },
	} {
		users := full
		change(&users)
		opts := base
		opts.Users = &users
		if r, err := NewRouter(opts); err == nil || r != nil {
			t.Fatalf("%s: incomplete user options accepted", name)
		}
	}
	opts := base
	opts.Users = &full
	if _, err := NewRouter(opts); err != nil {
		t.Fatal(err)
	}
}

func TestUserRoutesAbsentWithoutAccounts(t *testing.T) {
	h := newHarness(t)
	for _, req := range [][2]string{{http.MethodPost, "/compat/Users/AuthenticateByName"}, {http.MethodGet, "/compat/Users/Me"}, {http.MethodGet, "/compat/Users/Public"}, {http.MethodGet, "/compat/Users/" + testUserWire}, {http.MethodPost, "/compat/Sessions/Logout"}} {
		assertEmpty(t, req[1], h.do(req[0], req[1], authHeader(nativeToken)), http.StatusNotFound)
	}
}

func TestAuthenticateByName(t *testing.T) {
	h := newUserHarness(t)
	w := h.login(hdr("Authorization", loginHeader), "application/json", `{"Username":"viewer","Pw":"correct horse battery"}`)
	checkGolden(t, "users_authenticate_by_name.json", w)
	want := loginCall{name: "viewer", password: "correct horse battery", ip: testClientIP, client: domain.NativeClient{Name: "Test Player", Device: "Living Room", DeviceID: "device-1", Version: "1.2.3"}}
	if len(h.accounts.logins) != 1 || h.accounts.logins[0] != want {
		t.Fatalf("login call %+v", h.accounts.logins)
	}
	if len(h.limitArgs) != 1 || h.limitArgs[0] != [2]string{testClientIP, "viewer"} {
		t.Fatalf("limiter call %v", h.limitArgs)
	}
	if h.admitted != 1 || h.released != 1 || h.authCalls != 0 {
		t.Fatalf("admitted %d released %d lookups %d", h.admitted, h.released, h.authCalls)
	}
	// Legacy header, lower-case members, charset parameter, a stale token
	// and unknown members are all accepted like upstream.
	w = h.login(hdr(headerLegacyAuthorization, schemeLegacy+` Client=c, DeviceId=d, Token=stale`), "application/json; charset=utf-8", `{"username":"viewer","PW":"pw","Extra":1}`)
	if w.Code != http.StatusOK {
		t.Fatalf("legacy header: %d %s", w.Code, w.Body)
	}
	if got := h.accounts.logins[1]; got.name != "viewer" || got.password != "pw" || got.client != (domain.NativeClient{Name: "c", DeviceID: "d"}) {
		t.Fatalf("legacy header call %+v", got)
	}
	// Missing client identity is passed on; the account service refuses it
	// (400) before any password work, exactly like the native route.
	h.accounts.loginErr = domain.ErrInvalid
	assertGeneric(t, "no client", h.login(nil, "application/json", `{"Username":"viewer","Pw":"pw"}`), http.StatusBadRequest)
	if got := h.accounts.logins[2]; got.client != (domain.NativeClient{}) {
		t.Fatalf("no client call %+v", got)
	}
}

func TestAuthenticateByNameErrors(t *testing.T) {
	h := newUserHarness(t)
	body := `{"Username":"viewer","Pw":"x"}`
	header := hdr("Authorization", loginHeader)
	// Unknown account, wrong password, disabled, deleted and locked all come
	// back from the account service as ErrUnauthenticated: one answer.
	h.accounts.loginErr = domain.ErrUnauthenticated
	first := h.login(header, "application/json", body)
	assertEmpty(t, "unauthenticated", first, http.StatusUnauthorized)
	second := h.login(header, "application/json", `{"Username":"no-such-user","Pw":"x"}`)
	if second.Code != first.Code || second.Body.String() != first.Body.String() || len(second.Header()) != len(first.Header()) {
		t.Fatal("unknown and known accounts are distinguishable")
	}
	for err, status := range map[error]int{
		domain.ErrNativeLoginDisabled: http.StatusForbidden,
		domain.ErrSessionLimit:        http.StatusTooManyRequests,
		domain.ErrDatabase:            http.StatusServiceUnavailable,
		context.DeadlineExceeded:      http.StatusServiceUnavailable,
	} {
		h.accounts.loginErr = err
		assertEmpty(t, err.Error(), h.login(header, "application/json", body), status)
	}
	// The account password of an account with a second factor: a refusal
	// that names the application password route (G07.8).
	h.accounts.loginErr = fmt.Errorf("complete login: %w", domain.ErrSecondFactorRequired)
	if w := h.login(header, "application/json", body); w.Code != http.StatusForbidden || w.Header().Get("X-Jelee-Error") != "app_password_required" ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || !strings.Contains(w.Body.String(), "application password") || w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
		t.Fatalf("second factor refusal: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	h.accounts.loginErr = errors.New("boom 10.0.0.5 /var/lib/jelee")
	assertGeneric(t, "internal", h.login(header, "application/json", body), http.StatusInternalServerError)
	calls := len(h.accounts.logins)

	// Refused before the account service.
	for name, tc := range map[string]struct {
		header      http.Header
		contentType string
		body        string
		status      int
	}{
		"no password":       {header, "application/json", `{"Username":"viewer"}`, 400},
		"null password":     {header, "application/json", `{"Username":"viewer","Pw":null}`, 400},
		"no user name":      {header, "application/json", `{"Pw":"x"}`, 400},
		"empty body":        {header, "application/json", ``, 400},
		"not an object":     {header, "application/json", `[]`, 400},
		"trailing data":     {header, "application/json", body + `{}`, 400},
		"form body":         {header, "application/x-www-form-urlencoded", `Username=viewer&Pw=x`, 400},
		"no content type":   {header, "", body, 415},
		"text body":         {header, "text/plain", body, 415},
		"malformed header":  {hdr("Authorization", primary(`Client="a`+"\x01"+`"`)), "application/json", body, 400},
		"duplicate headers": {hdr("Authorization", loginHeader, "Authorization", loginHeader), "application/json", body, 400},
		"fetch site":        {hdr("Authorization", loginHeader, "Sec-Fetch-Site", "same-origin"), "application/json", body, 403},
		"fetch mode":        {hdr("Authorization", loginHeader, "Sec-Fetch-Mode", "cors"), "application/json", body, 403},
		"origin":            {hdr("Authorization", loginHeader, "Origin", "http://localhost"), "application/json", body, 403},
		"transcoding":       {header, "application/json", `{"Username":"viewer","Pw":"x","EnableTranscoding":true}`, 409},
	} {
		w := h.login(tc.header, tc.contentType, tc.body)
		if w.Code != tc.status {
			t.Fatalf("%s: %d, want %d", name, w.Code, tc.status)
		}
	}
	if len(h.accounts.logins) != calls {
		t.Fatal("refused request reached the account service")
	}

	// Shared rate limit: refused with Retry-After rounded up, no login work.
	h.limited = true
	w := h.login(header, "application/json", body)
	assertEmpty(t, "rate limited", w, http.StatusTooManyRequests)
	if w.Header().Get("Retry-After") != "3" || len(h.accounts.logins) != calls {
		t.Fatalf("rate limit: retry %q", w.Header().Get("Retry-After"))
	}
	h.limited = false

	// Shared admission budget: busy is 503 with Retry-After, no login work.
	h.busy = true
	w = h.login(header, "application/json", body)
	assertEmpty(t, "busy", w, http.StatusServiceUnavailable)
	if w.Header().Get("Retry-After") != "1" || len(h.accounts.logins) != calls {
		t.Fatal("busy budget reached the account service")
	}
	if h.admitted != h.released {
		t.Fatalf("admission leak: %d admitted, %d released", h.admitted, h.released)
	}
}

func TestCurrentUser(t *testing.T) {
	h := newUserHarness(t)
	checkGolden(t, "users_me.json", h.do(http.MethodGet, "/compat/Users/Me", authHeader(nativeToken)))
	checkGolden(t, "users_me.json", h.do(http.MethodGet, "/compat/users/me", authHeader(nativeToken)))
	if len(h.accounts.gets) != 2 || h.accounts.gets[0] != testUserID || h.accounts.actors[0] != (domain.Actor{UserID: testUserID, SessionID: testSessionID, IP: testClientIP}) {
		t.Fatalf("get calls %v %v", h.accounts.gets, h.accounts.actors)
	}
	assertEmpty(t, "no token", h.do(http.MethodGet, "/compat/Users/Me", nil), http.StatusUnauthorized)
	assertEmpty(t, "web token", h.do(http.MethodGet, "/compat/Users/Me", authHeader(webToken)), http.StatusUnauthorized)
	h.accounts.getErr = domain.ErrDatabase
	assertEmpty(t, "database", h.do(http.MethodGet, "/compat/Users/Me", authHeader(nativeToken)), http.StatusServiceUnavailable)
	if len(h.accounts.gets) != 3 || h.admitted != h.released {
		t.Fatal("unexpected account calls")
	}
}

func TestUserByID(t *testing.T) {
	h := newUserHarness(t)
	for _, id := range []string{testUserWire, testUserID, strings.ToUpper(testUserWire)} {
		checkGolden(t, "users_me.json", h.do(http.MethodGet, "/compat/Users/"+id, authHeader(nativeToken)))
	}
	checkGolden(t, "users_by_id_admin.json", h.do(http.MethodGet, "/compat/Users/"+strings.ReplaceAll(testOtherID, "-", ""), authHeader(adminToken)))
	// Another account is refused for a plain user whether or not it exists.
	assertEmpty(t, "other", h.do(http.MethodGet, "/compat/Users/"+strings.ReplaceAll(testOtherID, "-", ""), authHeader(nativeToken)), http.StatusForbidden)
	assertEmpty(t, "missing", h.do(http.MethodGet, "/compat/Users/"+strings.Repeat("ab", 16), authHeader(nativeToken)), http.StatusForbidden)
	calls := len(h.accounts.gets)
	for _, id := range []string{"x", strings.Repeat("0", 32), "{" + testUserID + "}", testUserWire + "0", "AuthenticateByName"} {
		assertGeneric(t, id, h.do(http.MethodGet, "/compat/Users/"+id, authHeader(nativeToken)), http.StatusBadRequest)
	}
	if len(h.accounts.gets) != calls {
		t.Fatal("malformed identifier reached the account service")
	}
	h.accounts.getErr = domain.ErrNotFound
	assertEmpty(t, "admin missing", h.do(http.MethodGet, "/compat/Users/"+testUserWire, authHeader(adminToken)), http.StatusNotFound)
	h.accounts.getErr, h.accounts.deleted = nil, true
	assertEmpty(t, "deleted", h.do(http.MethodGet, "/compat/Users/"+testUserWire, authHeader(adminToken)), http.StatusNotFound)
	assertEmpty(t, "no token", h.do(http.MethodGet, "/compat/Users/"+testUserWire, nil), http.StatusUnauthorized)
}

func TestPublicUsersIsEmpty(t *testing.T) {
	h := newUserHarness(t)
	for _, header := range []http.Header{nil, authHeader(nativeToken), authHeader(adminToken)} {
		w := h.do(http.MethodGet, "/compat/Users/Public", header)
		if w.Code != http.StatusOK || w.Body.String() != "[]" {
			t.Fatalf("public users: %d %s", w.Code, w.Body)
		}
	}
	if h.authCalls != 0 || h.admitted != 0 || len(h.accounts.gets) != 0 {
		t.Fatal("public users consulted sessions or accounts")
	}
}

func TestLogout(t *testing.T) {
	h := newUserHarness(t)
	assertEmpty(t, "no token", h.do(http.MethodPost, "/compat/Sessions/Logout", nil), http.StatusUnauthorized)
	assertEmpty(t, "web token", h.do(http.MethodPost, "/compat/Sessions/Logout", authHeader(webToken)), http.StatusUnauthorized)
	if len(h.accounts.revokes) != 0 {
		t.Fatal("unauthenticated logout revoked a session")
	}
	w := h.do(http.MethodPost, "/compat/sessions/logout", authHeader(nativeToken))
	assertEmpty(t, "logout", w, http.StatusNoContent)
	if len(h.accounts.revokes) != 1 || h.accounts.revokes[0] != [2]string{testUserID, testSessionID} || w.Header().Get("Content-Type") != "" {
		t.Fatalf("revokes %v", h.accounts.revokes)
	}
	assertEmpty(t, "GET logout", h.do(http.MethodGet, "/compat/Sessions/Logout", authHeader(nativeToken)), http.StatusMethodNotAllowed)
}

// No user module response publishes the client address or anything else
// about the host.
func TestUserResponsesPublishNoAddresses(t *testing.T) {
	h := newUserHarness(t)
	responses := []*httptest.ResponseRecorder{
		h.login(hdr("Authorization", loginHeader), "application/json", `{"Username":"viewer","Pw":"x"}`),
		h.do(http.MethodGet, "/compat/Users/Me", authHeader(nativeToken)),
		h.do(http.MethodGet, "/compat/Users/"+testUserWire, authHeader(adminToken)),
	}
	for _, w := range responses {
		body := w.Body.String()
		for _, pattern := range []*regexp.Regexp{ipv4Pattern, absPathPattern} {
			if m := pattern.FindString(body); m != "" {
				t.Fatalf("publishes %q in %s", m, body)
			}
		}
		for _, banned := range []string{testClientIP, "RemoteEndPoint", "LastLoginDate", "Locale", "zh-TW", "AllowNative", "CreatedAt"} {
			if strings.Contains(body, banned) {
				t.Fatalf("publishes %q in %s", banned, body)
			}
		}
	}
}

func TestUserRoutesAreWalkable(t *testing.T) {
	h := newUserHarness(t)
	seen := map[string]bool{}
	if err := chi.Walk(h.handler.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"POST /Users/AuthenticateByName", "GET /Users/Public", "GET /Users/Me", "GET /Users/{id}", "POST /Sessions/Logout"} {
		if !seen[route] {
			t.Fatalf("route %s not walkable: %v", route, seen)
		}
	}
	if len(seen) != 9 {
		t.Fatalf("unexpected routes %v", seen)
	}
}
