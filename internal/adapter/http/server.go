package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/i18n"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/go-chi/chi/v5"
)

type Backend interface {
	Ready(context.Context) error
	Authenticate(context.Context, string) (access.Principal, error)
}

type Server struct {
	cfg             config.Config
	backend         Backend
	catalog         *app.Catalog
	delivery        *media.Handler
	logger          *slog.Logger
	accounts        *app.Accounts
	loginLimiter    *LoginLimiter
	passwordLimiter *LoginLimiter
	accountSlots    chan struct{}
	jobs            *app.Jobs
	jobSlots        chan struct{}
}

func New(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, accounts ...*app.Accounts) (http.Handler, error) {
	var account *app.Accounts
	if len(accounts) == 1 {
		account = accounts[0]
	} else if len(accounts) > 1 {
		return nil, errors.New("only one account service may be provided")
	}
	return NewWithJobs(cfg, backend, catalog, resolver, logger, account, nil)
}

func NewWithJobs(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, account *app.Accounts, jobs *app.Jobs) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if backend == nil || catalog == nil || logger == nil {
		return nil, errors.New("HTTP dependencies must be provided")
	}
	delivery, err := media.NewHandler(resolver, media.Options{MaxConcurrent: cfg.MaxStreams, WriteTimeout: 30 * time.Second, LookupTimeout: cfg.RequestTimeout(), WriteError: WriteError})
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, backend: backend, catalog: catalog, delivery: delivery, logger: logger}
	if cfg.EnableAccounts {
		if account == nil {
			return nil, errors.New("account service must be provided")
		}
		s.accounts = account
		s.accountSlots = make(chan struct{}, cfg.Accounts.PasswordConcurrency*4)
		s.loginLimiter, err = NewLoginLimiter(LoginLimiterOptions{Window: time.Duration(cfg.Accounts.LoginWindowSeconds) * time.Second, IPLimit: cfg.Accounts.LoginIPLimit, UserLimit: cfg.Accounts.LoginUserLimit, MaxEntries: cfg.Accounts.LoginMaxEntries})
		if err != nil {
			return nil, err
		}
		s.passwordLimiter, err = NewLoginLimiter(LoginLimiterOptions{Window: time.Duration(cfg.Accounts.LoginWindowSeconds) * time.Second, IPLimit: cfg.Accounts.LoginIPLimit, UserLimit: cfg.Accounts.LoginUserLimit, MaxEntries: cfg.Accounts.LoginMaxEntries})
		if err != nil {
			return nil, err
		}
	}
	if cfg.EnableJobs {
		if jobs == nil {
			return nil, errors.New("job service must be provided")
		}
		s.jobs = jobs
		s.jobSlots = make(chan struct{}, cfg.Jobs.Workers*2+2)
	}
	r := chi.NewRouter()
	r.Use(s.boundary)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"status": "ok"}})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := backend.Ready(ctx); err != nil {
			writeProblem(w, r, 503, "not_ready", "Service is not ready.")
			return
		}
		writeJSON(w, 200, map[string]any{"data": map[string]string{"status": "ready"}})
	})
	r.Get("/api/v1/system", func(w http.ResponseWriter, r *http.Request) {
		probe := s.jobs.ProbeCapability()
		writeJSON(w, 200, map[string]any{"data": map[string]any{"name": "Jelee", "devMode": false, "probe": probe, "capabilities": map[string]any{"transcoding": false, "hls": false, "dash": false, "remux": false, "downloads": false, "directDelivery": cfg.EnableDirect, "catalog": cfg.EnableCatalog, "accounts": cfg.EnableAccounts, "inventoryScan": cfg.EnableJobs, "probe": probe.Available}}})
	})
	r.Get("/api-docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Jelee API</title><h1>Jelee API</h1><p>Experimental catalog and direct delivery API.</p><a href="/api/v1/openapi.json">OpenAPI 3.1 specification</a></html>`))
	})
	r.Get("/api/v1/openapi.json", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, Specification(cfg)) })
	if cfg.EnableAccounts {
		s.accountRoutes(r)
	}
	if cfg.EnableJobs {
		s.jobRoutes(r)
	}
	if cfg.EnableCatalog {
		r.Group(func(r chi.Router) {
			r.Use(s.authenticate)
			r.Get("/api/v1/items", s.list)
			r.Get("/api/v1/items/{id}", s.item)
			if cfg.EnableDirect {
				r.Get("/api/v1/sources/{id}/stream", s.stream)
				r.Head("/api/v1/sources/{id}/stream", s.stream)
			}
		})
	}
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { WriteError(w, r, domain.ErrNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) { WriteError(w, r, media.ErrMethodNotAllowed) })
	return r, nil
}

func (s *Server) boundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			writeProblem(w, r, 500, "internal_error", "Request could not be completed.")
			return
		}
		w.Header().Set("X-Request-ID", hex.EncodeToString(id))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Jelee-Dev-Mode", "false")
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		defer func() {
			if recover() != nil {
				s.logger.Error("request panic", "component", "http", "requestId", w.Header().Get("X-Request-ID"))
				writeProblem(w, r, 500, "internal_error", "Request could not be completed.")
			}
			s.logger.Info("request completed", "component", "http", "requestId", w.Header().Get("X-Request-ID"), "method", logging.SafeMethod(r.Method), "durationMs", time.Since(start).Milliseconds())
		}()
		host, valid := requestHost(r.Host)
		allowed := false
		for _, h := range s.cfg.AllowedHosts {
			if valid && h == host {
				allowed = true
				break
			}
		}
		if !allowed {
			writeProblem(w, r, 400, "invalid_host", "Host is not allowed.")
			return
		}
		// Never derive URLs or client privileges from forwarding or client supplied headers.
		if media.IsForbiddenDeliveryRoute(r.URL.Path) {
			WriteError(w, r, media.ErrTranscodeDisabled)
			return
		}
		if strings.Contains(strings.ToLower(r.URL.Path), "/debug/") {
			WriteError(w, r, domain.ErrNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || len(header) != 50 {
			WriteError(w, r, domain.ErrUnauthenticated)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
		p, err := s.backend.Authenticate(ctx, strings.TrimPrefix(header, "Bearer "))
		cancel()
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if app.ValidLocale(p.Locale) {
			r = r.Clone(r.Context())
			r.Header.Set("Accept-Language", p.Locale)
		}
		next.ServeHTTP(w, r.WithContext(access.WithPrincipal(r.Context(), p)))
	})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	query, err := strictQuery(r, "cursor", "limit")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	limit := 50
	if query["limit"] != "" {
		limit, err = strconv.Atoi(query["limit"])
		if err != nil {
			WriteError(w, r, domain.ErrInvalid)
			return
		}
	}
	p, _ := access.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	items, err := s.catalog.List(ctx, p.UserID, query["cursor"], limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) == limit {
		next = items[len(items)-1].ID
	}
	writeJSON(w, 200, map[string]any{"data": items, "pagination": map[string]any{"nextCursor": next, "limit": limit}})
}

func (s *Server) item(w http.ResponseWriter, r *http.Request) {
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	p, _ := access.PrincipalFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	defer cancel()
	item, err := s.catalog.Get(ctx, p.UserID, chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": item})
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if err := media.GuardProduction(r); err != nil {
		WriteError(w, r, err)
		return
	}
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	if !domain.ValidID(id) {
		WriteError(w, r, domain.ErrNotFound)
		return
	}
	s.delivery.ServeSource(w, r, id)
}

func strictQuery(r *http.Request, keys ...string) (map[string]string, error) {
	result := map[string]string{}
	// url.Values alone discards malformed fields; reject the raw parse error explicitly.
	values, err := parseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, domain.ErrInvalid
	}
	for key, vs := range values {
		allowed := false
		for _, k := range keys {
			if k == key {
				allowed = true
			}
		}
		if !allowed || len(vs) != 1 {
			return nil, domain.ErrInvalid
		}
		result[key] = vs[0]
	}
	return result, nil
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 500, "internal_error", "Request could not be completed."
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, media.ErrNotFound):
		status, code, message = 404, "not_found", "Resource was not found."
	case errors.Is(err, domain.ErrUnauthenticated), errors.Is(err, media.ErrUnauthenticated):
		status, code, message = 401, "authentication_required", "Authentication is required."
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, media.ErrInvalidRequest), errors.Is(err, password.ErrInvalidPassword):
		status, code, message = 400, "invalid_request", "Request is invalid."
	case errors.Is(err, domain.ErrForbidden):
		status, code, message = 403, "forbidden", "Operation is not permitted."
	case errors.Is(err, domain.ErrConflict):
		status, code, message = 409, "conflict", "Resource conflicts with existing state."
	case errors.Is(err, domain.ErrJobQueueFull):
		status, code, message = 429, "job_queue_full", "Job queue capacity reached. Try again later."
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, domain.ErrJobBusy):
		status, code, message = 409, "job_busy", "This library already has an active job."
	case errors.Is(err, domain.ErrScanUnavailable):
		status, code, message = 503, "scan_unavailable", "Scan root is unavailable."
	case errors.Is(err, domain.ErrScanLimit):
		status, code, message = 409, "scan_limit", "Scan resource limit reached."
	case errors.Is(err, domain.ErrProbeDisabled):
		status, code, message = 409, "probe_disabled", "Media probing is disabled."
	case errors.Is(err, domain.ErrProbeRuntimeUnavailable):
		status, code, message = 503, "probe_runtime_unavailable", "Media probing is unavailable. Check the isolated runtime."
	case errors.Is(err, domain.ErrProbeCacheCapacity):
		status, code, message = 409, "probe_cache_capacity", "Probe cache capacity reached."
	case errors.Is(err, domain.ErrProbeIdentityMismatch):
		status, code, message = 409, "probe_identity_mismatch", "Probe tool identity changed. Retry the job."
	case errors.Is(err, domain.ErrProbeInvalidated):
		status, code, message = 409, "probe_invalidated", "Probe scope changed. Retry the job."
	case errors.Is(err, domain.ErrLastAdmin):
		status, code, message = 409, "last_admin", "An active administrator must remain."
	case errors.Is(err, domain.ErrSessionLimit):
		status, code, message = 429, "session_limit", "Active session limit reached."
	case errors.Is(err, errAuthRateLimited):
		status, code, message = 429, "auth_rate_limited", "Too many authentication attempts. Try again later."
	case errors.Is(err, domain.ErrDatabase):
		status, code, message = 503, "not_ready", "Service is not ready."
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, code, message = 408, "request_timeout", "Request was cancelled or timed out."
	case errors.Is(err, media.ErrPlaybackDenied):
		status, code, message = 403, "web_playback_disabled", "This session cannot play media."
	case errors.Is(err, media.ErrTranscodeDisabled):
		status, code, message = 409, "transcode_disabled", "Only original direct delivery is supported."
	case errors.Is(err, media.ErrBusy):
		status, code, message = 429, "stream_limit", "Stream concurrency limit reached."
	case errors.Is(err, media.ErrLookupTimeout):
		status, code, message = 504, "lookup_timeout", "Media lookup timed out."
	case errors.Is(err, media.ErrMethodNotAllowed):
		status, code, message = 405, "method_not_allowed", "Method is not supported."
	case errors.Is(err, media.ErrInvalidRange):
		status, code, message = 416, "invalid_range", "Range cannot be satisfied."
	case errors.Is(err, media.ErrPreconditionFailed):
		status, code, message = 412, "precondition_failed", "Precondition failed."
	case errors.Is(err, media.ErrBodyTooLarge):
		status, code, message = 413, "body_too_large", "Request body exceeds the limit."
	case errors.Is(err, media.ErrUnsupportedMediaType):
		status, code, message = 415, "unsupported_media_type", "Request content type is not supported."
	}
	writeProblem(w, r, status, code, message)
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Content-Language", i18n.Locale(r.Header.Get("Accept-Language")))
	w.Header().Add("Vary", "Accept-Language")
	message = i18n.Message(code, r.Header.Get("Accept-Language"), message)
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": map[string]any{}, "traceId": w.Header().Get("X-Request-ID")}})
}

func requestHost(value string) (string, bool) {
	value = strings.ToLower(value)
	if value == "" || strings.ContainsAny(value, " /\\@\r\n\t") {
		return "", false
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		ip := strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
		return ip, net.ParseIP(ip) != nil
	}
	if strings.Contains(value, ":") {
		host, port, err := net.SplitHostPort(value)
		if err != nil {
			return "", false
		}
		if strings.HasPrefix(value, "[") && net.ParseIP(host) == nil {
			return "", false
		}
		for _, digit := range port {
			if digit < '0' || digit > '9' {
				return "", false
			}
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", false
		}
		return host, true
	}
	return value, !strings.ContainsAny(value, "[]")
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
