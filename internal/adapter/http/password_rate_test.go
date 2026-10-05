package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type wrongOldPasswordRepository struct{ app.AccountRepository }

func (wrongOldPasswordRepository) CredentialsFor(_ context.Context, a domain.Actor, _ string) (domain.Credentials, error) {
	return domain.Credentials{UserID: a.UserID, PasswordHash: "private-test-hash", Version: 1}, nil
}

func TestPasswordChangeIsThrottledByAuthenticatedUserAndPeerBeforeKDF(t *testing.T) {
	for _, dimension := range []string{"user", "peer"} {
		t.Run(dimension, func(t *testing.T) {
			repo := httpAccountRepository{AccountRepository: wrongOldPasswordRepository{}, credentials: func(context.Context, string) (domain.Credentials, error) {
				return domain.Credentials{}, domain.ErrNotFound
			}}
			f := newAccountHTTPFixture(t, repo, func(c *config.Config) {
				c.Accounts.LoginIPLimit = 100
				c.Accounts.LoginUserLimit = 100
				if dimension == "user" {
					c.Accounts.LoginUserLimit = 1
				} else {
					c.Accounts.LoginIPLimit = 1
				}
			})
			f.passwords.verify = func(context.Context, string, string) (bool, error) { return false, nil }
			body := `{"oldPassword":"wrong","newPassword":"replacement secret"}`
			first := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/password", body, "u"))
			// A wrong current password is an input error on a valid session, not
			// an expired credential: 400 invalid_password, never 401.
			if first.Code != 400 || !strings.Contains(first.Body.String(), `"code":"invalid_password"`) || f.passwords.verifyCalls != 1 {
				t.Fatalf("old password was not verified once or was misreported: %d %s", first.Code, first.Body.String())
			}
			second := accountRequest(http.MethodPut, "/api/v1/users/me/password", body, "u")
			if dimension == "user" {
				second.RemoteAddr = "198.51.100.24:4444"
			} else {
				f.backend.auth = func(context.Context, string) (access.Principal, error) {
					return access.Principal{UserID: itemID, SessionID: sessionID, Kind: access.ClientWeb}, nil
				}
			}
			second.Header.Set("X-Forwarded-For", "203.0.113.30")
			result := f.serve(second)
			if result.Code != 429 || result.Header().Get("Retry-After") == "" || !strings.Contains(result.Body.String(), "auth_rate_limited") || f.passwords.verifyCalls != 1 {
				t.Fatal("password-change throttle was bypassed or ran extra KDF")
			}
			// Failed old-password guesses must not consume the login namespace; a
			// stolen session must not prevent the real owner from logging in to revoke it.
			login := f.serve(accountRequest(http.MethodPost, "/api/v1/auth/login", `{"name":"owner","password":"wrong"}`, ""))
			if login.Code != 401 || f.passwords.dummyCalls != 1 {
				t.Fatal("password attempts incorrectly locked the login budget")
			}
		})
	}
}

func TestAccountScalarNullCannotResetPermissionsOrProfile(t *testing.T) {
	f := newAccountHTTPFixture(t, httpAccountRepository{}, nil)
	for _, tc := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/users/" + itemID, `{"name":"alice","locale":"en-US","admin":null,"disabled":null}`},
		{"PUT", "/api/v1/users/me/profile", `{"locale":"en-US","hidden":null}`},
		{"POST", "/api/v1/auth/rotate", `{"deviceName":null}`},
		{"POST", "/api/v1/users", `{"name":"alice","password":"long enough password","displayName":null}`},
	} {
		r := accountRequest(tc.method, tc.path, tc.body, "a")
		r.Header.Set("Idempotency-Key", "null-must-not-create")
		assertProblem(t, f.serve(r), 400, "invalid_request")
	}
	if f.passwords.hashCalls != 0 || f.passwords.verifyCalls != 0 {
		t.Fatal("null input reached password work")
	}
}

func TestPasswordChangeWrongOldPasswordIsNotAnExpiredSession(t *testing.T) {
	repo := httpAccountRepository{AccountRepository: wrongOldPasswordRepository{}}
	f := newAccountHTTPFixture(t, repo, nil)
	f.passwords.verify = func(context.Context, string, string) (bool, error) { return false, nil }
	for _, locale := range []struct{ tag, message string }{{"en-US", "Current password is incorrect."}, {"zh-TW", "目前密碼不正確。"}} {
		r := accountRequest(http.MethodPut, "/api/v1/users/me/password", `{"oldPassword":"wrong","newPassword":"replacement secret"}`, "u")
		r.Header.Set("Accept-Language", locale.tag)
		w := f.serve(r)
		assertProblem(t, w, 400, "invalid_password")
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Message != locale.message {
			t.Fatalf("%s message = %q", locale.tag, body.Error.Message)
		}
		// The session is still valid: no cookie is expired.
		if cookies := w.Result().Cookies(); len(cookies) != 0 {
			t.Fatalf("wrong old password touched the session cookie: %v", cookies)
		}
	}
	if f.passwords.hashCalls != 0 {
		t.Fatal("wrong old password reached hashing")
	}
}
