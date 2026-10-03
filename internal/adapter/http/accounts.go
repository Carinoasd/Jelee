package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

const accountBodyLimit = 64 << 10

var errAuthRateLimited = errors.New("authentication attempt rate limited")

type accountOperation func(http.ResponseWriter, *http.Request, domain.Actor) (any, int, error)

// Admission is non-blocking: each process permits at most four HTTP requests
// per password worker across all account routes, including body reads and KDF
// waits. A saturated service cannot accumulate an unbounded password queue.
func (s *Server) accountBudget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.accountSlots <- struct{}{}:
			defer func() { <-s.accountSlots }()
		case <-r.Context().Done():
			WriteError(w, r, r.Context().Err())
			return
		default:
			w.Header().Set("Retry-After", "1")
			writeProblem(w, r, 503, "account_busy", "Account service is busy. Try again later.")
			return
		}
		// Bound response writes too: a peer that stops reading must not hold an
		// admission slot forever. net/http clears this after finishing the response.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(s.cfg.RequestTimeout()))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accountEndpoint(admin, listQuery bool, operation accountOperation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := access.PrincipalFromContext(r.Context())
		if !ok {
			WriteError(w, r, domain.ErrUnauthenticated)
			return
		}
		if admin && !principal.Admin {
			WriteError(w, r, domain.ErrForbidden)
			return
		}
		if !listQuery {
			if _, err := strictQuery(r); err != nil {
				WriteError(w, r, err)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
		defer cancel()
		r = r.WithContext(ctx)
		// Account bodies are small. Bound network reads separately from the request
		// context, since cancellation alone does not unblock a slow HTTP body reader.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.cfg.RequestTimeout()))
		actor := domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID, IP: requestClientIP(r)}
		data, status, err := operation(w, r, actor)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if status == http.StatusNoContent {
			w.WriteHeader(status)
			return
		}
		writeJSON(w, status, map[string]any{"data": data})
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	r = r.WithContext(ctx)
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.cfg.RequestTimeout()))
	var input struct {
		Name       string  `json:"name"`
		Password   *string `json:"password"`
		DeviceName string  `json:"deviceName"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		WriteError(w, r, err)
		return
	}
	if input.Password == nil {
		WriteError(w, r, domain.ErrInvalid)
		return
	}
	ip := requestClientIP(r)
	if allowed, retry := s.loginLimiter.Allow(ip, input.Name); !allowed {
		seconds := int64((retry + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeProblem(w, r, 429, "auth_rate_limited", "Too many login attempts. Try again later.")
		return
	}
	grant, err := s.accounts.Login(ctx, input.Name, *input.Password, input.DeviceName, ip)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": grant})
}

// POST actions without input require an empty JSON object. DELETE actions
// require an empty body. Both reject unknown input rather than silently ignore it.
func emptyAccountInput(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodDelete {
		if r.Body == nil {
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(data) != 0 {
			return domain.ErrInvalid
		}
		return r.Context().Err()
	}
	return DecodeJSON(w, r, &struct{}{}, accountBodyLimit)
}

func (s *Server) accountRoutes(r chi.Router) {
	r.With(s.accountBudget).Post("/api/v1/auth/login", s.login)
	r.Group(func(r chi.Router) {
		r.Use(s.accountBudget)
		r.Use(s.authenticate)
		r.Post("/api/v1/auth/logout", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			return nil, 204, s.accounts.Revoke(r.Context(), a, a.UserID, a.SessionID)
		}))
		r.Post("/api/v1/auth/rotate", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			var input struct {
				DeviceName string `json:"deviceName"`
			}
			if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
				return nil, 0, err
			}
			grant, err := s.accounts.Rotate(r.Context(), a, input.DeviceName)
			return grant, 200, err
		}))
		r.Get("/api/v1/users/me", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			user, err := s.accounts.Get(r.Context(), a, a.UserID)
			return user, 200, err
		}))
		r.Put("/api/v1/users/me/profile", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			var input struct {
				DisplayName string `json:"displayName"`
				Locale      string `json:"locale"`
				Hidden      bool   `json:"hidden"`
			}
			if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
				return nil, 0, err
			}
			user, err := s.accounts.Profile(r.Context(), a, domain.ProfileInput{DisplayName: input.DisplayName, Locale: input.Locale, Hidden: input.Hidden})
			return user, 200, err
		}))
		r.Put("/api/v1/users/me/password", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			var input struct {
				OldPassword *string `json:"oldPassword"`
				NewPassword *string `json:"newPassword"`
			}
			if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
				return nil, 0, err
			}
			if input.OldPassword == nil || input.NewPassword == nil {
				return nil, 0, domain.ErrInvalid
			}
			// Use the authenticated immutable ID, not a client-selected name. This
			// budget is separate from login so a stolen token cannot lock login out.
			if allowed, retry := s.passwordLimiter.Allow(a.IP, a.UserID); !allowed {
				seconds := int64((retry + time.Second - 1) / time.Second)
				if seconds < 1 {
					seconds = 1
				}
				w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
				return nil, 0, errAuthRateLimited
			}
			return nil, 204, s.accounts.ChangePassword(r.Context(), a, *input.OldPassword, *input.NewPassword)
		}))
		r.Get("/api/v1/users", s.accountEndpoint(true, true, s.listUsers))
		r.Post("/api/v1/users", s.accountEndpoint(true, false, s.createUser))
		r.Get("/api/v1/users/{id}", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			user, err := s.accounts.Get(r.Context(), a, chi.URLParam(r, "id"))
			return user, 200, err
		}))
		r.Put("/api/v1/users/{id}", s.accountEndpoint(true, false, s.updateUser))
		r.Delete("/api/v1/users/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			return nil, 204, s.accounts.Delete(r.Context(), a, chi.URLParam(r, "id"))
		}))
		r.Post("/api/v1/users/{id}/restore", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			user, err := s.accounts.Restore(r.Context(), a, chi.URLParam(r, "id"))
			return user, 200, err
		}))
		r.Post("/api/v1/users/{id}/unlock", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			return nil, 204, s.accounts.Unlock(r.Context(), a, chi.URLParam(r, "id"))
		}))
		r.Get("/api/v1/users/{id}/sessions", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			sessions, err := s.accounts.Sessions(r.Context(), a, chi.URLParam(r, "id"))
			return sessions, 200, err
		}))
		r.Delete("/api/v1/users/{id}/sessions", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			return nil, 204, s.accounts.RevokeAll(r.Context(), a, chi.URLParam(r, "id"))
		}))
		r.Delete("/api/v1/users/{id}/sessions/{sessionID}", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			return nil, 204, s.accounts.Revoke(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "sessionID"))
		}))
		r.Get("/api/v1/users/{id}/libraries", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			libraries, err := s.accounts.Libraries(r.Context(), a, chi.URLParam(r, "id"))
			return libraries, 200, err
		}))
		r.Put("/api/v1/users/{id}/libraries", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			var input struct {
				LibraryIDs []string `json:"libraryIds"`
			}
			if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
				return nil, 0, err
			}
			if input.LibraryIDs == nil {
				return nil, 0, domain.ErrInvalid
			}
			return nil, 204, s.accounts.SetLibraries(r.Context(), a, chi.URLParam(r, "id"), input.LibraryIDs)
		}))
	})
}

type userSettings struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Locale      string `json:"locale"`
	Hidden      bool   `json:"hidden"`
	Admin       bool   `json:"admin"`
	Disabled    bool   `json:"disabled"`
}

func (u userSettings) domainInput() domain.UserInput {
	return domain.UserInput{Name: u.Name, DisplayName: u.DisplayName, Locale: u.Locale, Hidden: u.Hidden, Admin: u.Admin, Disabled: u.Disabled}
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input struct {
		userSettings
		Password string `json:"password"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 {
		return nil, 0, domain.ErrInvalid
	}
	if input.Locale == "" {
		input.Locale = "zh-CN"
	}
	user, replayed, err := s.accounts.Create(r.Context(), a, input.domainInput(), input.Password, keys[0])
	status := 201
	if replayed {
		status = 200
		w.Header().Set("Idempotency-Replayed", "true")
	}
	return user, status, err
}
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input userSettings
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	user, err := s.accounts.Update(r.Context(), a, chi.URLParam(r, "id"), input.domainInput())
	return user, 200, err
}
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	query, err := strictQuery(r, "cursor", "limit", "includeDeleted")
	if err != nil {
		return nil, 0, err
	}
	limit := 50
	if value, ok := query["limit"]; ok {
		limit, err = strconv.Atoi(value)
		if err != nil {
			return nil, 0, domain.ErrInvalid
		}
	}
	deleted := false
	if value, ok := query["includeDeleted"]; ok {
		if value != "true" && value != "false" {
			return nil, 0, domain.ErrInvalid
		}
		deleted = value == "true"
	}
	users, err := s.accounts.List(r.Context(), a, query["cursor"], limit, deleted)
	if err != nil {
		return nil, 0, err
	}
	next := ""
	if len(users) == limit {
		next = users[len(users)-1].ID
	}
	return map[string]any{"users": users, "pagination": map[string]any{"nextCursor": next, "limit": limit}}, 200, nil
}
