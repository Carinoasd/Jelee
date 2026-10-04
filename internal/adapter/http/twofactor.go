package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// secondFactorChallenge answers a web login whose password verified for an
// account with a second factor (G07.8): no session, no cookie, only the
// challenge for POST /api/v1/auth/login/second-factor.
type secondFactorChallenge struct {
	Required bool `json:"secondFactorRequired"`
	domain.LoginChallenge
}

// allowSecondFactor draws one code attempt from the second factor budget,
// which is separate from the password budgets: codes cannot spend the login
// budget of the account, and the login budget does not limit codes.
func (s *Server) allowSecondFactor(w http.ResponseWriter, ip, key string) error {
	allowed, retry := s.secondFactorLimiter.Allow(ip, key)
	if allowed {
		return nil
	}
	seconds := int64((retry + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	return errAuthRateLimited
}

// secondFactorLogin completes a web login challenge with an authenticator
// code or a recovery code and issues the web session like /auth/login.
func (s *Server) secondFactorLogin(w http.ResponseWriter, r *http.Request) {
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	r = r.WithContext(ctx)
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.cfg.RequestTimeout()))
	var input struct {
		Challenge    *string `json:"challenge"`
		Code         string  `json:"code"`
		RecoveryCode string  `json:"recoveryCode"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		WriteError(w, r, err)
		return
	}
	if input.Challenge == nil || (input.Code == "") == (input.RecoveryCode == "") {
		WriteError(w, r, domain.ErrInvalid)
		return
	}
	if s.clients != nil {
		if err := s.clients.admitLogin(r, clientLabels{}); err != nil {
			WriteError(w, r, err)
			return
		}
	}
	ip := requestClientIP(r)
	if err := s.allowSecondFactor(w, ip, "challenge:"+*input.Challenge); err != nil {
		WriteError(w, r, err)
		return
	}
	grant, err := s.accounts.CompleteLogin(ctx, *input.Challenge, input.Code, input.RecoveryCode, ip)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": issueWebGrant(w, grant)})
}

// twoFactorRoutes are the authenticated second factor and application
// password routes. Self-service changes need a web session; the store
// refuses native sessions with 403.
func (s *Server) twoFactorRoutes(r chi.Router) {
	r.Get("/api/v1/users/{id}/two-factor", s.accountEndpoint(false, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		status, err := s.accounts.TwoFactor(r.Context(), a, chi.URLParam(r, "id"))
		return status, 200, err
	}))
	r.Delete("/api/v1/users/{id}/two-factor", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		return nil, 204, s.accounts.ResetTwoFactor(r.Context(), a, chi.URLParam(r, "id"))
	}))
	r.Post("/api/v1/users/me/two-factor/enroll", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		enrollment, err := s.accounts.BeginTwoFactor(r.Context(), a)
		return enrollment, 200, err
	}))
	codeRoute := func(run func(context.Context, domain.Actor, string) (domain.RecoveryCodes, error)) accountOperation {
		return func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			var input struct {
				Code *string `json:"code"`
			}
			if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
				return nil, 0, err
			}
			if input.Code == nil {
				return nil, 0, domain.ErrInvalid
			}
			if err := s.allowSecondFactor(w, a.IP, "user:"+a.UserID); err != nil {
				return nil, 0, err
			}
			codes, err := run(r.Context(), a, *input.Code)
			return codes, 200, err
		}
	}
	r.Post("/api/v1/users/me/two-factor/confirm", s.accountEndpoint(false, false, codeRoute(s.accounts.ConfirmTwoFactor)))
	r.Post("/api/v1/users/me/two-factor/recovery-codes", s.accountEndpoint(false, false, codeRoute(s.accounts.RegenerateRecoveryCodes)))
	r.Post("/api/v1/users/me/two-factor/disable", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Password     *string `json:"password"`
			Code         string  `json:"code"`
			RecoveryCode string  `json:"recoveryCode"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.Password == nil || (input.Code == "") == (input.RecoveryCode == "") {
			return nil, 0, domain.ErrInvalid
		}
		// One budget for the password and the code: both are verified by
		// every attempt.
		if err := s.allowSecondFactor(w, a.IP, "user:"+a.UserID); err != nil {
			return nil, 0, err
		}
		return nil, 204, s.accounts.DisableTwoFactor(r.Context(), a, *input.Password, input.Code, input.RecoveryCode)
	}))
	r.Get("/api/v1/users/{id}/app-passwords", s.accountEndpoint(false, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		passwords, err := s.accounts.AppPasswords(r.Context(), a, chi.URLParam(r, "id"))
		return passwords, 200, err
	}))
	r.Post("/api/v1/users/me/app-passwords", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Name *string `json:"name"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.Name == nil {
			return nil, 0, domain.ErrInvalid
		}
		created, err := s.accounts.CreateAppPassword(r.Context(), a, *input.Name)
		return created, 201, err
	}))
	r.Delete("/api/v1/users/{id}/app-passwords/{appPasswordId}", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		return nil, 204, s.accounts.DeleteAppPassword(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "appPasswordId"))
	}))
}
