package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// A verified password of an account with a second factor answers the
// challenge: no session fields, no cookie.
func TestLoginAnswersSecondFactorChallenge(t *testing.T) {
	expires := time.Date(2026, 10, 4, 12, 5, 0, 0, time.UTC)
	repo := httpAccountRepository{
		credentials: func(context.Context, string) (domain.Credentials, error) {
			return domain.Credentials{UserID: userID, Name: "alice", PasswordHash: "$argon2id$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA", Version: 1}, nil
		},
		login: func(context.Context, domain.LoginInput) (domain.SessionGrant, error) {
			return domain.SessionGrant{Challenge: &domain.LoginChallenge{Token: strings.Repeat("c", 43), ExpiresAt: expires}}, nil
		},
	}
	f := newAccountHTTPFixture(t, repo, nil)
	f.passwords.verify = func(context.Context, string, string) (bool, error) { return true, nil }
	w := f.serve(accountRequest("POST", "/api/v1/auth/login", `{"name":"alice","password":"correct horse battery"}`, ""))
	if w.Code != 200 || len(w.Result().Cookies()) != 0 {
		t.Fatalf("challenge: %d %v", w.Code, w.Result().Cookies())
	}
	want := `{"data":{"secondFactorRequired":true,"challenge":"` + strings.Repeat("c", 43) + `","expiresAt":"2026-10-04T12:05:00Z"}}`
	if strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("body %s", w.Body.String())
	}
}

// The second step has its own budget per address and challenge, and the
// account service decides only after it: without second factor storage the
// answer is 409 two_factor_unavailable, never a session.
func TestSecondFactorLoginBudgetAndValidation(t *testing.T) {
	f := newAccountHTTPFixture(t, httpAccountRepository{}, func(c *config.Config) { c.Accounts.LoginUserLimit = 3 })
	challenge := strings.Repeat("c", 43)
	body := `{"challenge":"` + challenge + `","code":"123456"}`
	for _, invalid := range []string{`{"code":"123456"}`, `{"challenge":"` + challenge + `"}`, `{"challenge":"` + challenge + `","code":"1","recoveryCode":"2"}`, `{"challenge":"` + challenge + `","code":"123456","extra":1}`} {
		assertProblem(t, f.serve(accountRequest("POST", "/api/v1/auth/login/second-factor", invalid, "")), 400, "invalid_request")
	}
	for range 3 {
		assertProblem(t, f.serve(accountRequest("POST", "/api/v1/auth/login/second-factor", body, "")), 409, "two_factor_unavailable")
	}
	w := f.serve(accountRequest("POST", "/api/v1/auth/login/second-factor", body, ""))
	assertProblem(t, w, 429, "auth_rate_limited")
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	// Another challenge has its own bucket.
	other := `{"challenge":"` + strings.Repeat("d", 43) + `","recoveryCode":"abcd-efgh-ijkl-mnop"}`
	assertProblem(t, f.serve(accountRequest("POST", "/api/v1/auth/login/second-factor", other, "")), 409, "two_factor_unavailable")
}

// Code attempts of the signed-in user share one budget per user and address.
func TestSecondFactorSelfServiceBudget(t *testing.T) {
	f := newAccountHTTPFixture(t, httpAccountRepository{}, func(c *config.Config) { c.Accounts.LoginUserLimit = 2 })
	for range 2 {
		assertProblem(t, f.serve(accountRequest("POST", "/api/v1/users/me/two-factor/confirm", `{"code":"123456"}`, "u")), 409, "two_factor_unavailable")
	}
	assertProblem(t, f.serve(accountRequest("POST", "/api/v1/users/me/two-factor/disable", `{"password":"x","code":"123456"}`, "u")), 429, "auth_rate_limited")
	assertProblem(t, f.serve(accountRequest("POST", "/api/v1/users/me/two-factor/recovery-codes", `{"code":"123456"}`, "u")), 429, "auth_rate_limited")
	for _, invalid := range []string{`{}`, `{"code":null}`} {
		assertProblem(t, f.serve(accountRequest("POST", "/api/v1/users/me/two-factor/confirm", invalid, "a")), 400, "invalid_request")
	}
	assertProblem(t, f.serve(accountRequest("POST", "/api/v1/users/me/two-factor/disable", `{"password":"x"}`, "a")), 400, "invalid_request")
	assertProblem(t, f.serve(accountRequest("POST", "/api/v1/users/me/app-passwords", `{}`, "a")), 400, "invalid_request")
	assertProblem(t, f.serve(accountRequest("POST", "/api/v1/users/me/two-factor/enroll", `{"x":1}`, "a")), 400, "invalid_request")
	// Without second factor storage every operation is unavailable, not a
	// silent success.
	assertProblem(t, f.serve(accountRequest("GET", "/api/v1/users/"+userID+"/two-factor", "", "u")), 409, "two_factor_unavailable")
	assertProblem(t, f.serve(accountRequest("POST", "/api/v1/users/me/two-factor/enroll", `{}`, "a")), 409, "two_factor_unavailable")
	assertProblem(t, f.serve(accountRequest("POST", "/api/v1/users/me/app-passwords", `{"name":"TV"}`, "a")), 409, "two_factor_unavailable")
	// Administrator-only reset.
	assertProblem(t, f.serve(accountRequest("DELETE", "/api/v1/users/"+userID+"/two-factor", "", "u")), 403, "forbidden")
}
