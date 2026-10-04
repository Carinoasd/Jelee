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
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/compat"
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

// sessionUseTracker is implemented by backends that record when and from
// which address a session was last used (G07.4). Such bookkeeping is
// throttled and best effort; it never changes the authentication result.
type sessionUseTracker interface {
	AuthenticateFrom(ctx context.Context, token, ip string) (access.Principal, error)
}

type Server struct {
	trustedProxies  []netip.Prefix
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
	metadata        *app.Metadata
	metrics         http.Handler
	metricsSlots    chan struct{}
	images          *app.Images
	imageSlots      chan struct{}
	web             *webApp
	compat          http.Handler
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

func NewWithJobs(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, account *app.Accounts, jobs *app.Jobs, metadataServices ...*app.Metadata) (http.Handler, error) {
	if len(metadataServices) > 1 {
		return nil, errors.New("only one metadata service may be provided")
	}
	var metadata *app.Metadata
	if len(metadataServices) == 1 {
		metadata = metadataServices[0]
	}
	return newServer(cfg, backend, catalog, resolver, logger, account, jobs, metadata, nil, nil, nil)
}

func NewWithTelemetry(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, account *app.Accounts, jobs *app.Jobs, metadata *app.Metadata, metrics http.Handler) (http.Handler, error) {
	return NewWithImages(cfg, backend, catalog, resolver, logger, account, jobs, metadata, metrics, nil)
}

func NewWithImages(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, account *app.Accounts, jobs *app.Jobs, metadata *app.Metadata, metrics http.Handler, images *app.Images) (http.Handler, error) {
	return newServer(cfg, backend, catalog, resolver, logger, account, jobs, metadata, metrics, images, nil)
}

func NewWithResources(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, account *app.Accounts, jobs *app.Jobs, metadata *app.Metadata, metrics http.Handler, images *app.Images, budget app.WorkBudget) (http.Handler, error) {
	if budget == nil {
		return nil, errors.New("shared resource budget must be provided")
	}
	return newServer(cfg, backend, catalog, resolver, logger, account, jobs, metadata, metrics, images, budget)
}

func newServer(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, account *app.Accounts, jobs *app.Jobs, metadata *app.Metadata, metrics http.Handler, images *app.Images, budget app.WorkBudget) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if backend == nil || catalog == nil || logger == nil {
		return nil, errors.New("HTTP dependencies must be provided")
	}
	// A resolver backed by shared storage also rechecks sessions of running
	// streams, so revocation through any instance cuts them (G07.4).
	sessions, _ := resolver.(media.SessionChecker)
	if resolver != nil && cfg.Access.HiddenContentStatus() == http.StatusForbidden {
		resolver = hiddenContentResolver{resolver}
	}
	delivery, err := media.NewHandler(resolver, media.Options{Budget: budget, MaxConcurrent: cfg.MaxStreams, WriteTimeout: 30 * time.Second, LookupTimeout: cfg.RequestTimeout(), WriteError: WriteError,
		Sessions: sessions, SessionCheckInterval: cfg.Streaming.RevokeCheckInterval(), Limits: deliveryLimits(cfg.Streaming)})
	if err != nil {
		return nil, err
	}
	prefixes, err := cfg.TrustedProxyPrefixes()
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, backend: backend, catalog: catalog, delivery: delivery, logger: logger, trustedProxies: prefixes}
	if s.web, err = newWebApp(cfg.WebDir); err != nil {
		return nil, err
	}
	if cfg.EnableAccounts {
		s.metadata = metadata
	}
	if cfg.EnableAccounts && cfg.TMDBAPIKey != "" {
		if !metadata.HasProvider() {
			return nil, errors.New("metadata service must be provided")
		}
	}
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
	if cfg.EnableMetrics {
		if metrics == nil {
			return nil, errors.New("metrics handler must be provided")
		}
		s.metrics = metrics
		s.metricsSlots = make(chan struct{}, 2)
	}
	if cfg.EnableImages {
		if images == nil {
			return nil, errors.New("image service must be provided")
		}
		s.images = images
		s.imageSlots = make(chan struct{}, cfg.Images.MaxConcurrent)
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
		writeJSON(w, 200, map[string]any{"data": map[string]any{"name": "Jelee", "devMode": false, "probe": probe, "capabilities": map[string]any{"transcoding": false, "hls": false, "dash": false, "remux": false, "downloads": false, "dlna": false, "discovery": false, "liveTv": false, "epg": false, "tuners": false, "recordings": false, "channels": false, "directDelivery": cfg.EnableDirect, "catalog": cfg.EnableCatalog, "accounts": cfg.EnableAccounts, "inventoryScan": cfg.EnableJobs, "probe": probe.Available}}})
	})
	r.Get("/api-docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := `<!doctype html><html lang="en"><meta charset="utf-8"><title>Jelee API</title><h1>Jelee API</h1><p>Experimental catalog and direct delivery API.</p><a href="/api/v1/openapi.json">OpenAPI 3.1 specification</a>`
		if cfg.TMDBAPIKey != "" {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src https://www.themoviedb.org; frame-ancestors 'none'; base-uri 'none'")
			page += `<section aria-label="Credits"><h2>Credits</h2><a href="https://www.themoviedb.org"><img width="64" alt="TMDB" src="https://www.themoviedb.org/assets/v4/logos/v2/blue_short-8e7b30f73a4020692ccca9c88bafe5dcb6f8a62a4c6bc55cd9ba82bb2cd95f6c.svg"></a><p>This product uses the TMDB API but is not endorsed or certified by TMDB.</p></section>`
		}
		_, _ = w.Write([]byte(page + "</html>"))
	})
	r.Get("/api/v1/openapi.json", openAPIHandler(cfg))
	if cfg.EnableAccounts {
		s.accountRoutes(r)
	}
	if cfg.EnableMetrics {
		s.metricsRoutes(r)
	}
	if cfg.EnableImages {
		s.imageRoutes(r)
	}
	if s.metadata != nil && cfg.TMDBAPIKey != "" {
		s.metadataRoutes(r)
	}
	if cfg.EnableAccounts {
		s.itemMetadataRoutes(r)
	}
	if cfg.EnableJobs {
		s.jobRoutes(r)
	}
	if cfg.EnableCompat {
		if s.compat, err = s.newCompat(cfg, backend); err != nil {
			return nil, err
		}
		r.Mount(compat.Prefix, s.compat)
	}
	if cfg.EnableCatalog {
		r.Group(func(r chi.Router) {
			r.Use(s.authenticate)
			r.Get("/api/v1/items", s.list)
			r.Get("/api/v1/items/{id}", s.item)
			if cfg.EnableDirect {
				r.Get("/api/v1/sources/{id}/stream", s.stream)
				r.Head("/api/v1/sources/{id}/stream", s.stream)
				r.Get(subtitleTrackRoute, s.track(media.TrackSubtitle))
				r.Head(subtitleTrackRoute, s.track(media.TrackSubtitle))
				r.Get(audioTrackRoute, s.track(media.TrackAudio))
				r.Head(audioTrackRoute, s.track(media.TrackAudio))
				r.Get("/api/v1/items/{id}/playback", s.playbackInfo)
				r.Post("/api/v1/items/{id}/playback/check", s.playbackCheck)
			}
		})
	}
	// The frontend has no routes of its own: a GET or HEAD outside /api that no
	// API route claims is answered from the web directory when one is set.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		// The compatibility prefix is reserved: a letter-case variant of it is
		// served by the layer, and nothing below it ever reaches the frontend.
		if compat.HasPrefix(r.URL.Path) {
			if s.compat != nil {
				s.compat.ServeHTTP(w, r)
				return
			}
			WriteError(w, r, domain.ErrNotFound)
			return
		}
		if s.web.handles(r) {
			s.web.serve(w, r)
			return
		}
		WriteError(w, r, domain.ErrNotFound)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) { WriteError(w, r, media.ErrMethodNotAllowed) })
	return r, nil
}

// newCompat builds the third-party client compatibility layer. It reuses the
// server's session lookup and its transcode_disabled envelope and, with the
// account service enabled, the server's own native login, admission budget,
// login rate limiter and client address: the layer has no account logic of
// its own.
func (s *Server) newCompat(cfg config.Config, backend Backend) (http.Handler, error) {
	id := cfg.CompatServerID
	if id == "" {
		id = compat.DeriveServerID(cfg.AllowedHosts...)
	}
	authenticate := backend.Authenticate
	if tracker, ok := backend.(sessionUseTracker); ok {
		// Record last use like the native API does. The boundary middleware
		// already stored the proxy-aware client address in the request context
		// the compat layer derives its lookup context from.
		authenticate = func(ctx context.Context, token string) (access.Principal, error) {
			address, _ := ctx.Value(clientAddressKey{}).(string)
			return tracker.AuthenticateFrom(ctx, token, address)
		}
	}
	opts := compat.Options{Authenticate: authenticate, WriteRejection: WriteError, ServerID: id, Timeout: cfg.RequestTimeout()}
	if s.accounts != nil {
		opts.Users = &compat.UserOptions{Accounts: s.accounts, Admit: s.admitAccount, AllowLogin: s.loginLimiter.Allow, ClientIP: requestClientIP}
	}
	// The library module reads only through the catalog service, which
	// applies the library grants in storage like the native catalog routes.
	if cfg.EnableCatalog && s.catalog.CanBrowse() {
		opts.Library = &compat.LibraryOptions{Catalog: s.catalog, HiddenStatus: cfg.Access.HiddenContentStatus(), DirectPlay: cfg.EnableDirect, ClientIP: requestClientIP}
	}
	return compat.NewRouter(opts)
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
		r = s.withClientAddress(r, w.Header().Get("X-Request-ID"))
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
		if compat.RemovedFeaturePath(r.URL.Path) {
			writeProblem(w, r, 501, "feature_removed", "Discovery, live TV, recordings and channels are not supported.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An Authorization header always wins and keeps its exact behavior; the
		// browser session cookie is consulted only when no header is present.
		method := authMethod{}
		if len(r.Header.Values("Authorization")) > 0 {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") || len(header) != 50 {
				WriteError(w, r, domain.ErrUnauthenticated)
				return
			}
			method.token = strings.TrimPrefix(header, "Bearer ")
		} else if cookie, present := sessionCookieToken(r); present {
			if !validSessionToken(cookie) {
				clearSessionCookie(w)
				WriteError(w, r, domain.ErrUnauthenticated)
				return
			}
			method = authMethod{cookie: true, token: cookie}
		} else {
			WriteError(w, r, domain.ErrUnauthenticated)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
		var p access.Principal
		var err error
		if tracker, ok := s.backend.(sessionUseTracker); ok {
			p, err = tracker.AuthenticateFrom(ctx, method.token, requestClientIP(r))
		} else {
			p, err = s.backend.Authenticate(ctx, method.token)
		}
		cancel()
		if method.cookie && (errors.Is(err, domain.ErrUnauthenticated) || err == nil && p.Kind != access.ClientWeb) {
			// Only web sessions may ride on a cookie. A native credential in the
			// cookie is refused exactly like an unknown one, and a stale cookie is
			// expired so the browser stops sending it.
			clearSessionCookie(w)
			WriteError(w, r, domain.ErrUnauthenticated)
			return
		}
		if err != nil {
			WriteError(w, r, err)
			return
		}
		// Cookies are attached by the browser automatically, so every unsafe
		// method authenticated by one must prove same-origin intent (G35.1).
		// Bearer requests carry an explicit credential and need no token.
		if method.cookie && !safeMethod(r.Method) && !validCSRF(r, method.token) {
			writeProblem(w, r, 403, "csrf_failed", "Security token is missing or invalid. Reload the page and try again.")
			return
		}
		if app.ValidLocale(p.Locale) {
			r = r.Clone(r.Context())
			r.Header.Set("Accept-Language", p.Locale)
		}
		next.ServeHTTP(w, r.WithContext(withAuthMethod(access.WithPrincipal(r.Context(), p), method)))
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
		WriteError(w, r, s.hiddenContentError(err))
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

// External track routes (G10.9, G15.5, G16.4). They share the production
// guard, the native-only rule and the whole direct delivery path of stream.
const (
	subtitleTrackRoute = "/api/v1/sources/{id}/subtitles/{trackId}"
	audioTrackRoute    = "/api/v1/sources/{id}/audio/{trackId}"
)

// trackURL is the direct delivery URL of one external track, relative to the
// API origin as clients address every other route.
func trackURL(sourceID, kind, trackID string) string {
	segment := "audio"
	if kind == domain.SidecarKindSubtitle {
		segment = "subtitles"
	}
	return "/api/v1/sources/" + sourceID + "/" + segment + "/" + trackID
}

func (s *Server) track(kind media.TrackKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := media.GuardProduction(r); err != nil {
			WriteError(w, r, err)
			return
		}
		if _, err := strictQuery(r); err != nil {
			WriteError(w, r, err)
			return
		}
		id, trackID := chi.URLParam(r, "id"), chi.URLParam(r, "trackId")
		if !domain.ValidID(id) || !domain.ValidID(trackID) {
			WriteError(w, r, domain.ErrNotFound)
			return
		}
		s.delivery.ServeTrack(w, r, id, kind, trackID)
	}
}

// deliveryLimits maps the per-limit switches of G45.4 onto the delivery handler.
func deliveryLimits(c config.StreamingConfig) media.Limits {
	return media.Limits{StreamLimit: c.EnableStreamLimit, MaxStreamsPerUser: c.MaxStreamsPerUser, MaxStreamsPerDevice: c.MaxStreamsPerDevice,
		BandwidthLimit: c.EnableBandwidthLimit, MaxKbpsPerUser: c.MaxKbpsPerUser, BandwidthPerDevice: c.BandwidthScope == "device"}
}

// hiddenContentError maps a direct media lookup miss to the configured
// hidden-content status (G48.3). Lookups apply authorization in SQL and cannot
// tell a missing ID from an invisible one, so both get the same answer and the
// response never confirms existence. Only the explicit 403 opt-in changes it.
func (s *Server) hiddenContentError(err error) error {
	if s.cfg.Access.HiddenContentStatus() == http.StatusForbidden && (errors.Is(err, domain.ErrNotFound) || errors.Is(err, media.ErrNotFound)) {
		return domain.ErrForbidden
	}
	return err
}

// hiddenContentResolver applies the 403 opt-in to authorized source lookups
// only. File-level failures after a successful lookup keep their own status.
type hiddenContentResolver struct{ media.Resolver }

func (h hiddenContentResolver) Resolve(ctx context.Context, p access.Principal, id string) (media.Source, error) {
	source, err := h.Resolver.Resolve(ctx, p, id)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, media.ErrNotFound) {
		return media.Source{}, domain.ErrForbidden
	}
	return source, err
}

func (h hiddenContentResolver) ResolveTrack(ctx context.Context, p access.Principal, sourceID string, kind media.TrackKind, trackID string) (media.Source, error) {
	source, err := h.Resolver.ResolveTrack(ctx, p, sourceID, kind, trackID)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, media.ErrNotFound) {
		return media.Source{}, domain.ErrForbidden
	}
	return source, err
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
	case errors.Is(err, domain.ErrNFODisabled):
		status, code, message = 409, "nfo_disabled", "NFO validation is disabled for this library."
	case errors.Is(err, domain.ErrIgnoreUnavailable):
		status, code, message = 503, "ignore_unavailable", "Ignore scanning is unavailable."
	case errors.Is(err, domain.ErrNFOReaderUnavailable):
		status, code, message = 503, "nfo_reader_unavailable", "NFO validation is unavailable."
	case errors.Is(err, domain.ErrNFOCacheCapacity):
		status, code, message = 409, "nfo_cache_capacity", "NFO cache capacity reached."
	case errors.Is(err, domain.ErrNFOIdentityMismatch):
		status, code, message = 409, "nfo_identity_mismatch", "NFO validation version changed. Retry the job."
	case errors.Is(err, domain.ErrNFOInvalidated):
		status, code, message = 409, "nfo_invalidated", "NFO validation scope changed. Retry the job."
	case errors.Is(err, domain.ErrLastAdmin):
		status, code, message = 409, "last_admin", "An active administrator must remain."
	case errors.Is(err, domain.ErrNativeLoginDisabled):
		status, code, message = 403, "native_login_disabled", "Native device login is not enabled for this account."
	case errors.Is(err, domain.ErrSessionLimit):
		status, code, message = 429, "session_limit", "Active session limit reached."
	case errors.Is(err, errAuthRateLimited):
		status, code, message = 429, "auth_rate_limited", "Too many authentication attempts. Try again later."
	case errors.Is(err, domain.ErrDatabase):
		status, code, message = 503, "not_ready", "Service is not ready."
	case errors.Is(err, domain.ErrMetadataUnavailable):
		status, code, message = 503, "metadata_unavailable", "Metadata provider is unavailable. Try again later."
	case errors.Is(err, domain.ErrImageBusy):
		status, code, message = 503, "image_busy", "Image processing is busy. Try again later."
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, domain.ErrImageUnavailable):
		status, code, message = 404, "image_unavailable", "Image is unavailable."
	case errors.Is(err, domain.ErrImageTooLarge):
		status, code, message = 413, "image_too_large", "Image exceeds the processing limit."
	case errors.Is(err, domain.ErrImageUnsupported):
		status, code, message = 415, "image_unsupported", "Image format is not supported."
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, code, message = 408, "request_timeout", "Request was cancelled or timed out."
	case errors.Is(err, media.ErrPlaybackDenied):
		status, code, message = 403, "web_playback_disabled", "This session cannot play media."
	case errors.Is(err, media.ErrTranscodeDisabled):
		status, code, message = 409, "transcode_disabled", "Only original direct delivery is supported."
	case errors.Is(err, media.ErrBusy):
		status, code, message = 429, "stream_limit", "Stream concurrency limit reached."
	case errors.Is(err, media.ErrUserStreamLimit):
		status, code, message = 429, "user_stream_limit", "Concurrent playback limit for this account reached."
	case errors.Is(err, media.ErrDeviceStreamLimit):
		status, code, message = 429, "device_stream_limit", "Concurrent playback limit for this device reached."
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
