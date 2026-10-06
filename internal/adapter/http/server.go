package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
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
	"github.com/MoYuanCN/Jelee/internal/platform/buildinfo"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/MoYuanCN/Jelee/internal/platform/i18n"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
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
	trustedProxies []netip.Prefix
	cfg            config.Config
	backend        Backend
	catalog        *app.Catalog
	delivery       *media.Handler
	// extracted serves embedded subtitles and fonts (G15.5, G15.7); nil
	// when mkvtoolnix extraction is not available or not enabled.
	extracted       media.ExtractedResolver
	logger          *slog.Logger
	accounts        *app.Accounts
	loginLimiter    *LoginLimiter
	passwordLimiter *LoginLimiter
	// secondFactorLimiter bounds authenticator and recovery code attempts,
	// separately from the password budgets (G07.8).
	secondFactorLimiter *LoginLimiter
	accountSlots        chan struct{}
	jobs                *app.Jobs
	jobSlots            chan struct{}
	metadata            *app.Metadata
	metrics             http.Handler
	metricsSlots        chan struct{}
	images              *app.Images
	imageSlots          chan struct{}
	web                 *webApp
	compat              http.Handler
	webhooks            *app.Webhooks
	clients             *ClientControl
	// setup is the G18 wizard and request gate; nil when not wired.
	setup *setupGate
	// router answers whether an API route claims a path (setup gate).
	router *chi.Mux
	// fontPolicy builds the frontend CSP from the site's external font
	// allowlist; nil without accounts.
	fontPolicy *frontendPolicy
	// dev is the developer mode controller (G45); nil unless this instance
	// meets its own developer mode thresholds, so production never mounts a
	// developer route or applies a relaxation.
	dev *devmode.Controller
	// shareAccess throttles the access records of guest sessions (G48.6).
	shareAccess shareAccessLog
	// userDataExports bounds the personal data exports streamed at once
	// (G07.7).
	userDataExports userDataExportGate
	// repair runs the self-healing repair actions (G50.4); nil when not
	// wired. repairTemplate holds the configured bounds.
	repair         *app.Repairer
	repairTemplate app.RepairOptions
	// readiness reports the dependency states of /readyz (G50.5); nil
	// reports none.
	readiness func(context.Context) map[string]string
	// deprecations is the G49.2 deprecation table (apiDeprecations unless a
	// test replaces it); deprecationIndex keys it by route.
	deprecations     []Deprecation
	deprecationIndex map[string]Deprecation
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
	return newServer(cfg, backend, catalog, resolver, logger, account, jobs, metadata, nil, nil, nil, nil)
}

func NewWithTelemetry(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, account *app.Accounts, jobs *app.Jobs, metadata *app.Metadata, metrics http.Handler) (http.Handler, error) {
	return NewWithImages(cfg, backend, catalog, resolver, logger, account, jobs, metadata, metrics, nil)
}

func NewWithImages(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, account *app.Accounts, jobs *app.Jobs, metadata *app.Metadata, metrics http.Handler, images *app.Images, options ...Option) (http.Handler, error) {
	return newServer(cfg, backend, catalog, resolver, logger, account, jobs, metadata, metrics, images, nil, options)
}

func NewWithResources(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, account *app.Accounts, jobs *app.Jobs, metadata *app.Metadata, metrics http.Handler, images *app.Images, budget app.WorkBudget, options ...Option) (http.Handler, error) {
	if budget == nil {
		return nil, errors.New("shared resource budget must be provided")
	}
	return newServer(cfg, backend, catalog, resolver, logger, account, jobs, metadata, metrics, images, budget, options)
}

func newServer(cfg config.Config, backend Backend, catalog *app.Catalog, resolver media.Resolver, logger *slog.Logger, account *app.Accounts, jobs *app.Jobs, metadata *app.Metadata, metrics http.Handler, images *app.Images, budget app.WorkBudget, options []Option) (http.Handler, error) {
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
	prefixes, err := cfg.TrustedProxyPrefixes()
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, backend: backend, catalog: catalog, logger: logger, trustedProxies: prefixes}
	s.shareAccess.seed = maphash.MakeSeed()
	s.deprecations = apiDeprecations
	for _, option := range options {
		option(s)
	}
	if err = validateDeprecations(s.deprecations); err != nil {
		return nil, fmt.Errorf("%w: %v", errDeprecationTable, err)
	}
	s.deprecationIndex = deprecationIndex(s.deprecations)
	if err = s.configureDevMode(); err != nil {
		return nil, err
	}
	limits := deliveryLimits(cfg.Streaming)
	if s.dev != nil {
		limits.RelaxStreams = func() bool { return s.dev.Effective(devmode.RelaxPlaybackConcurrency) }
		limits.RelaxBandwidth = func() bool { return s.dev.Effective(devmode.RelaxBandwidthLimit) }
	}
	if s.delivery, err = media.NewHandler(resolver, media.Options{Budget: budget, MaxConcurrent: cfg.MaxStreams, WriteTimeout: 30 * time.Second, LookupTimeout: cfg.RequestTimeout(), WriteError: WriteError,
		Sessions: sessions, SessionCheckInterval: cfg.Streaming.RevokeCheckInterval(), Limits: limits}); err != nil {
		return nil, err
	}
	if !cfg.EnableAccounts {
		// The wizard creates the administrator, which needs accounts.
		s.setup = nil
	}
	if cfg.EnableWebhooks && s.webhooks == nil {
		return nil, errors.New("webhook service must be provided")
	}
	switch {
	case !cfg.Matroska.EnableExtraction:
		s.extracted = nil
	case s.extracted == nil:
		// Configured routes keep their meaning without a runtime: every
		// embedded item is answered like a missing one.
		s.extracted = unavailableExtracted{}
	}
	if s.clients != nil {
		if !cfg.EnableAccounts {
			return nil, errors.New("client control needs the account service")
		}
		if _, ok := backend.(clientAuthenticator); !ok {
			return nil, errors.New("client control needs a backend that reports session clients")
		}
	}
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
		loginOptions := LoginLimiterOptions{Window: time.Duration(cfg.Accounts.LoginWindowSeconds) * time.Second, IPLimit: cfg.Accounts.LoginIPLimit, UserLimit: cfg.Accounts.LoginUserLimit, MaxEntries: cfg.Accounts.LoginMaxEntries}
		if s.dev != nil {
			loginOptions.Relaxed = func() bool { return s.dev.Effective(devmode.RelaxLoginRateLimit) }
		}
		s.loginLimiter, err = NewLoginLimiter(loginOptions)
		if err != nil {
			return nil, err
		}
		s.passwordLimiter, err = NewLoginLimiter(LoginLimiterOptions{Window: time.Duration(cfg.Accounts.LoginWindowSeconds) * time.Second, IPLimit: cfg.Accounts.LoginIPLimit, UserLimit: cfg.Accounts.LoginUserLimit, MaxEntries: cfg.Accounts.LoginMaxEntries})
		if err != nil {
			return nil, err
		}
		s.secondFactorLimiter, err = NewLoginLimiter(LoginLimiterOptions{Window: time.Duration(cfg.Accounts.LoginWindowSeconds) * time.Second, IPLimit: cfg.Accounts.LoginIPLimit, UserLimit: cfg.Accounts.LoginUserLimit, MaxEntries: cfg.Accounts.LoginMaxEntries})
		if err != nil {
			return nil, err
		}
		s.fontPolicy = newFrontendPolicy(account.SiteFontHosts)
		if s.web != nil {
			s.web.policy = s.fontPolicy
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
	s.router = r
	r.Use(s.boundary)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"status": "ok"}})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		// G50.5: the dependency states are fixed codes only (no versions,
		// counts, addresses or error text), so every caller may see them;
		// only the database and its schema decide readiness.
		if err := backend.Ready(ctx); err != nil {
			writeProblemDetails(w, r, 503, "not_ready", "Service is not ready.", s.readinessDetails(ctx))
			return
		}
		data := map[string]any{"status": "ready"}
		// An instance waiting for setup is ready: it must receive traffic so
		// the wizard is reachable. setup tells orchestrators and clients why
		// everything else answers 503 setup_required.
		if s.setup != nil {
			completed, err := s.setup.isCompleted(ctx)
			if err != nil {
				writeProblemDetails(w, r, 503, "not_ready", "Service is not ready.", s.readinessDetails(ctx))
				return
			}
			data["setup"] = "required"
			if completed {
				data["setup"] = "completed"
			}
		}
		if details := s.readinessDetails(ctx); len(details) > 0 {
			data["checks"] = details["checks"]
		}
		writeJSON(w, 200, map[string]any{"data": data})
	})
	r.Get("/api/v1/system", func(w http.ResponseWriter, r *http.Request) {
		probe := s.jobs.ProbeCapability()
		data := map[string]any{"name": "Jelee", "version": buildinfo.Version(), "devMode": false, "probe": probe, "capabilities": map[string]any{"transcoding": false, "hls": false, "dash": false, "remux": false, "downloads": false, "dlna": false, "discovery": false, "liveTv": false, "epg": false, "tuners": false, "recordings": false, "channels": false, "directDelivery": cfg.EnableDirect, "catalog": cfg.EnableCatalog, "accounts": cfg.EnableAccounts, "inventoryScan": cfg.EnableJobs, "probe": probe.Available}}
		// G45.3: the session and its deadline are public, like the header,
		// so every client can warn its user. Toggles stay administrator-only.
		if st := s.devStatus(); st.Active {
			data["devMode"], data["devModeExpiresAt"] = true, st.ExpiresAt.UTC().Format(time.RFC3339)
		}
		writeJSON(w, 200, map[string]any{"data": data})
	})
	r.Get("/api-docs", apiDocsHandler(cfg, s.deprecations))
	r.Get("/api/v1/openapi.json", openAPIHandler(cfg, s.deprecations...))
	if cfg.EnableAccounts {
		s.accountRoutes(r)
		// Without a wizard the instance counts as set up: the wizard paths
		// exist with the account rollout and answer 410 like after setup.
		s.setupRoutes(r)
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
	if cfg.EnableWebhooks {
		s.webhookRoutes(r)
	}
	if cfg.EnableAccounts {
		s.clientControlRoutes(r)
		s.shareRoutes(r)
	}
	if s.dev != nil {
		s.devRoutes(r)
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
			r.Get("/api/v1/items/{id}/details", s.itemDetails)
			r.Get("/api/v1/items/{id}/sources", s.itemSources)
			s.progressRoutes(r)
			s.watchStatsRoutes(r)
			s.versionRoutes(r)
			s.collectionRoutes(r)
			if cfg.EnableDirect {
				r.Get("/api/v1/sources/{id}/stream", s.stream)
				r.Head("/api/v1/sources/{id}/stream", s.stream)
				r.Get(subtitleTrackRoute, s.track(media.TrackSubtitle))
				r.Head(subtitleTrackRoute, s.track(media.TrackSubtitle))
				r.Get(audioTrackRoute, s.track(media.TrackAudio))
				r.Head(audioTrackRoute, s.track(media.TrackAudio))
				if cfg.Matroska.EnableExtraction {
					r.Get(embeddedSubtitleRoute, s.extractedRoute(media.ExtractedSubtitle, "index"))
					r.Head(embeddedSubtitleRoute, s.extractedRoute(media.ExtractedSubtitle, "index"))
					r.Get(attachmentRoute, s.extractedRoute(media.ExtractedAttachment, "attachmentId"))
					r.Head(attachmentRoute, s.extractedRoute(media.ExtractedAttachment, "attachmentId"))
					if cfg.SubtitleOCR.Enable {
						r.Get(ocrSubtitleRoute, s.extractedRoute(media.ExtractedOCRSubtitle, "index"))
						r.Head(ocrSubtitleRoute, s.extractedRoute(media.ExtractedOCRSubtitle, "index"))
					}
				}
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
	if gate, ok := backend.(clientAuthenticator); ok && s.clients != nil {
		// Record last use like the native API does, and apply client control
		// (G47) to native sessions. The layer refuses any other session kind
		// itself, so those are not gated or recorded here.
		authenticate = func(ctx context.Context, token string) (access.Principal, error) {
			address, _ := ctx.Value(clientAddressKey{}).(string)
			p, client, version, err := gate.AuthenticateClient(ctx, token, address)
			if err != nil || p.Kind != access.ClientNative {
				return p, err
			}
			var libraries []string
			if req := clientRequestFrom(ctx); req != nil {
				if libraries, err = s.clients.decide(ctx, req, gateInput{principal: p, session: client, version: version, token: token, authenticated: true}); err != nil {
					return access.Principal{}, err
				}
			}
			p.Request = requestScope(address, p.Kind, libraries)
			return p, nil
		}
	} else if tracker, ok := backend.(sessionUseTracker); ok {
		// Record last use like the native API does. The boundary middleware
		// already stored the proxy-aware client address in the request context
		// the compat layer derives its lookup context from.
		authenticate = func(ctx context.Context, token string) (access.Principal, error) {
			address, _ := ctx.Value(clientAddressKey{}).(string)
			p, err := tracker.AuthenticateFrom(ctx, token, address)
			p.Request = requestScope(address, p.Kind, nil)
			return p, err
		}
	} else {
		plain := authenticate
		authenticate = func(ctx context.Context, token string) (access.Principal, error) {
			address, _ := ctx.Value(clientAddressKey{}).(string)
			p, err := plain(ctx, token)
			p.Request = requestScope(address, p.Kind, nil)
			return p, err
		}
	}
	// Share guests (G48.6) use the native API only: the layer answers
	// their credentials like unknown ones.
	scoped := authenticate
	authenticate = func(ctx context.Context, token string) (access.Principal, error) {
		p, err := scoped(ctx, token)
		if err == nil && p.ShareID != "" {
			return access.Principal{}, domain.ErrUnauthenticated
		}
		return p, err
	}
	opts := compat.Options{Authenticate: authenticate, WriteRejection: WriteError, ServerID: id, Timeout: cfg.RequestTimeout()}
	if s.accounts != nil {
		opts.Users = &compat.UserOptions{Accounts: s.accounts, Admit: s.admitAccount, AllowLogin: s.loginLimiter.Allow, ClientIP: requestClientIP}
		if s.clients != nil {
			opts.Users.AdmitClient = func(r *http.Request) error {
				var labels clientLabels
				if auth, err := compat.ParseClientAuth(r.Header, nil); err == nil {
					labels = clientLabels{app: auth.Client, version: auth.Version, deviceID: auth.DeviceID, deviceName: auth.Device}
				}
				return s.clients.admitLogin(r, labels)
			}
		}
	}
	// The library module reads only through the catalog service, which
	// applies the library grants in storage like the native catalog routes.
	if cfg.EnableCatalog && s.catalog.CanBrowse() {
		opts.Library = &compat.LibraryOptions{Catalog: s.catalog, HiddenStatus: cfg.Access.HiddenContentStatus(), DirectPlay: cfg.EnableDirect, ClientIP: requestClientIP}
		// Playback reports, played marks and user data use the catalog's
		// progress buffer, which answers 503 until it is wired.
		opts.Library.Playstate = s.catalog
		// The playback module streams through the same delivery handler as
		// the native stream routes, so limits, revocation and Range
		// handling are shared rather than duplicated.
		if cfg.EnableDirect {
			opts.Library.Delivery = s.delivery
			if s.extracted != nil {
				opts.Library.Extracted = s.extractedResolver()
			}
		}
		// Item images use the /images pipeline, admission and deadline.
		if cfg.EnableImages && s.images != nil {
			opts.Library.Images = compatImages{s: s}
		}
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
		devActive := s.dev != nil && s.dev.Active()
		w.Header().Set("X-Jelee-Dev-Mode", strconv.FormatBool(devActive))
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		// G46.6: the request ID is the trace ID of the request's root span,
		// so logs, the error envelope and audit rows share one value.
		traceCtx, span := tracing.Default().StartRequest(r.Context(), w.Header().Get("X-Request-ID"), "http.request", "http")
		r = r.WithContext(traceCtx)
		defer func() {
			outcome := "ok"
			if recover() != nil {
				outcome = "panic"
				s.logger.ErrorContext(traceCtx, "request panic", "component", "http", "requestId", w.Header().Get("X-Request-ID"))
				writeProblem(w, r, 500, "internal_error", "Request could not be completed.")
			}
			s.logger.InfoContext(traceCtx, "request completed", "component", "http", "requestId", w.Header().Get("X-Request-ID"), "method", logging.SafeMethod(r.Method), "durationMs", time.Since(start).Milliseconds())
			span.End(outcome)
		}()
		r = s.withClientAddress(r, w.Header().Get("X-Request-ID"))
		if s.clients != nil && compat.HasPrefix(r.URL.Path) {
			// The compatibility layer hands the client control gate only a
			// context; the native API passes the request itself.
			r = r.WithContext(context.WithValue(r.Context(), clientRequestKey{}, newClientRequest(r)))
		}
		host, valid := requestHost(r.Host)
		allowed := false
		for _, h := range s.cfg.AllowedHosts {
			if valid && h == host {
				allowed = true
				break
			}
		}
		if !allowed && valid && devActive && s.dev.Effective(devmode.RelaxHostStrict) {
			// G45.4 relax_host_strict: any well-formed Host is served.
			allowed = true
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
		// Only the exact pprof prefix of a developer capable instance reaches
		// a handler, which answers 404 itself unless the session, the
		// debug_pprof toggle and the caller allow it (G45.5).
		if strings.Contains(strings.ToLower(r.URL.Path), "/debug/") && (s.dev == nil || !strings.HasPrefix(r.URL.Path, devPprofPrefix)) {
			WriteError(w, r, domain.ErrNotFound)
			return
		}
		if compat.RemovedFeaturePath(r.URL.Path) {
			writeProblem(w, r, 501, "feature_removed", "Discovery, live TV, recordings and channels are not supported.")
			return
		}
		// G18.4: a half-initialised instance serves nothing but the wizard.
		if s.setup != nil && !s.setupAllows(w, r) {
			return
		}
		s.deprecationHeaders(w, r)
		if devActive && s.dev.Effective(devmode.DebugBodyLogging) {
			s.logBodies(next, w, r)
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
		var client access.SessionClient
		var version int64
		gate, gated := s.backend.(clientAuthenticator)
		gated = gated && s.clients != nil
		if gated {
			p, client, version, err = gate.AuthenticateClient(ctx, method.token, requestClientIP(r))
		} else if tracker, ok := s.backend.(sessionUseTracker); ok {
			p, err = tracker.AuthenticateFrom(ctx, method.token, requestClientIP(r))
		} else {
			p, err = s.backend.Authenticate(ctx, method.token)
		}
		if method.cookie && (errors.Is(err, domain.ErrUnauthenticated) || err == nil && p.Kind != access.ClientWeb) {
			// Only web sessions may ride on a cookie. A native credential in the
			// cookie is refused exactly like an unknown one, and a stale cookie is
			// expired so the browser stops sending it.
			cancel()
			clearSessionCookie(w)
			WriteError(w, r, domain.ErrUnauthenticated)
			return
		}
		var libraries []string
		if err == nil && gated {
			req := clientRequestFrom(r.Context())
			if req == nil {
				req = newClientRequest(r)
			}
			libraries, err = s.clients.decide(ctx, req, gateInput{principal: p, session: client, version: version, token: method.token, authenticated: true})
			if method.cookie && errors.Is(err, domain.ErrUnauthenticated) {
				// A forced relogin revoked the session behind the cookie.
				clearSessionCookie(w)
			}
		}
		cancel()
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
		// The unified storage filter reads the request's network attributes
		// and client control library set with the principal (G48.5).
		p.Request = requestScope(requestClientIP(r), p.Kind, libraries)
		if p.ShareID != "" && !s.guestGate(w, r, p) {
			return
		}
		if app.ValidLocale(p.Locale) {
			r = r.Clone(r.Context())
			r.Header.Set("Accept-Language", p.Locale)
		}
		next.ServeHTTP(w, r.WithContext(withAuthMethod(access.WithPrincipal(r.Context(), p), method)))
	})
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
	case errors.Is(err, domain.ErrClientBlocked):
		status, code, message = 403, "client_blocked", "This client is not allowed to access the server."
	case errors.Is(err, domain.ErrClientPending):
		status, code, message = 403, "client_pending_approval", "This client is waiting for administrator approval."
	case errors.Is(err, domain.ErrClientReadOnly):
		status, code, message = 403, "client_read_only", "This client may only read."
	case errors.Is(err, domain.ErrShareUnavailable):
		status, code, message = 404, "share_unavailable", "This share link is unknown, revoked or expired."
	case errors.Is(err, domain.ErrShareForbidden):
		status, code, message = 403, "share_forbidden", "A share link does not allow this operation."
	case errors.Is(err, domain.ErrShareReadOnly):
		status, code, message = 403, "share_read_only", "This share link is read-only."
	case errors.Is(err, domain.ErrSharePlaybackDisabled):
		status, code, message = 403, "share_playback_disabled", "This share link does not allow playback."
	case errors.Is(err, domain.ErrClientRateLimited):
		status, code, message = 429, "client_rate_limited", "Too many requests from this client. Try again later."
		seconds := int64(1)
		var retry clientRetryError
		if errors.As(err, &retry) {
			if n := int64((retry.retry + time.Second - 1) / time.Second); n > 1 {
				seconds = n
			}
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	case errors.Is(err, errDevInactive):
		status, code, message = 409, "devmode_inactive", "Developer mode is not active."
	case errors.Is(err, errDevToggleUnavailable):
		status, code, message = 409, "devmode_toggle_unavailable", "This developer mode option is not available in this build."
	case errors.Is(err, errConfirmationRequired):
		status, code, message = 400, "confirmation_required", "This operation is dangerous and needs an explicit confirmation."
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
	case errors.Is(err, domain.ErrSecondFactorRequired):
		status, code, message = 403, "app_password_required", "Two-factor authentication is enabled for this account. Sign in on this device with an application password created in the web interface."
	case errors.Is(err, domain.ErrSecondFactorMismatch):
		status, code, message = 400, "invalid_two_factor_code", "The verification code or recovery code is incorrect or was already used."
	case errors.Is(err, domain.ErrChallengeInvalid):
		status, code, message = 401, "login_challenge_invalid", "The sign-in step expired or was already used. Sign in again with your password."
	case errors.Is(err, domain.ErrSecondFactorUnavailable):
		status, code, message = 409, "two_factor_unavailable", "Two-factor authentication is unavailable: the server has no master key configured."
	case errors.Is(err, domain.ErrSessionLimit):
		status, code, message = 429, "session_limit", "Active session limit reached."
	case errors.Is(err, domain.ErrPasswordMismatch):
		status, code, message = 400, "invalid_password", "Current password is incorrect."
	case errors.Is(err, domain.ErrVersionIdentityConflict):
		status, code, message = 409, "version_identity_conflict", "Items name different works: their external IDs or episode numbers disagree."
	case errors.Is(err, domain.ErrVersionMergeIncompatible):
		status, code, message = 409, "version_merge_incompatible", "Items cannot be combined: they differ in kind, library or series, hold other items, or the version is the only one."
	case errors.Is(err, domain.ErrVersionUndoUnavailable):
		status, code, message = 409, "version_undo_unavailable", "This operation can no longer be undone, or a later one must be undone first."
	case errors.Is(err, domain.ErrVersionItemBusy):
		status, code, message = 409, "version_item_busy", "The item is being played or processed. Try again later."
	case errors.Is(err, domain.ErrCustomCSSRejected):
		status, code, message = 400, "custom_css_rejected", "Custom CSS was refused: it contains markup, escapes, control characters or unbalanced blocks, or is too long."
	case errors.Is(err, errAuthRateLimited):
		status, code, message = 429, "auth_rate_limited", "Too many authentication attempts. Try again later."
	case errors.Is(err, domain.ErrWebhookTargetDenied):
		status, code, message = 400, "webhook_target_denied", "Webhook URL must be HTTPS to an allowed public host."
	case errors.Is(err, domain.ErrPlaybackBusy):
		status, code, message = 503, "playback_busy", "Playback reporting is busy. Try again later."
		w.Header().Set("Retry-After", "5")
	case errors.Is(err, domain.ErrWatchStatsExportLimit):
		status, code, message = 409, "stats_export_limit", "Export exceeds the row limit. Narrow the range."
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
	writeProblemDetails(w, r, status, code, message, map[string]any{})
}

// writeProblemDetails writes the error envelope with structured details.
// Details hold fixed codes and field paths only, never request values.
func writeProblemDetails(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	w.Header().Set("Content-Language", i18n.Locale(r.Header.Get("Accept-Language")))
	w.Header().Add("Vary", "Accept-Language")
	message = i18n.Message(code, r.Header.Get("Accept-Language"), message)
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": details, "traceId": w.Header().Get("X-Request-ID")}})
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
