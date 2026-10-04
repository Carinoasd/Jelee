package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/compat"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// G18 setup wizard API and request gate.
//
// Until setup completes every request is refused with 503 setup_required
// except liveness/readiness, discovery (/api/v1/system, the OpenAPI document,
// /api-docs), the frontend shell and the wizard API itself. After completion
// the wizard API answers 410 setup_completed for good.
//
// The wizard API is guarded by a one-time setup token generated at startup
// (see the runtime) and sent in SetupTokenHeader. A source-address allow list
// was rejected: behind Docker port mapping or a reverse proxy the wizard's
// legitimate client is not loopback, and allowing the proxy would allow the
// whole internet. Only the operator who can read the server's console or the
// token file can drive the wizard.

// SetupTokenHeader carries the one-time setup token.
const SetupTokenHeader = "X-Jelee-Setup-Token"

const (
	setupBodyLimit = 64 << 10
	// setupRecheckInterval bounds how long an instance keeps answering
	// setup_required after another instance or the CLI completed setup.
	setupRecheckInterval = time.Second
)

// SetupWizard is the app.Setup surface the HTTP adapter drives.
type SetupWizard interface {
	Status(context.Context) (domain.SetupStatus, error)
	SubmitLanguage(context.Context, string) (domain.SetupState, error)
	SubmitAdmin(context.Context, app.SetupAdminInput, string) (domain.SetupState, error)
	SubmitDatabase(context.Context) (domain.SetupState, error)
	SubmitMedia(context.Context, []domain.SetupLibrary) (domain.SetupState, error)
	SubmitTMDB(context.Context, domain.SetupTMDB) (domain.SetupState, error)
	SubmitToolchain(context.Context, bool) (domain.SetupState, error)
	SubmitMetadataPolicy(context.Context, domain.SetupMetadataPolicy) (domain.SetupState, error)
	SubmitNetwork(context.Context, domain.SetupNetwork) (domain.SetupState, error)
	Back(context.Context) (domain.SetupState, error)
	Finish(context.Context) (domain.SetupState, error)
}

// WithSetup mounts the wizard and the setup gate. token is the one-time
// setup token; empty means setup was already complete at startup, so the
// wizard can never be driven by this process. The option takes effect only
// with the account rollout, because the wizard creates the administrator.
func WithSetup(wizard SetupWizard, token string) Option {
	return func(s *Server) {
		if wizard != nil {
			s.setup = &setupGate{wizard: wizard, token: []byte(token), now: time.Now}
			// No token: setup was complete at startup and completion is final,
			// so requests never need to consult setup storage.
			s.setup.completed.Store(token == "")
		}
	}
}

type setupGate struct {
	wizard    SetupWizard
	token     []byte
	now       func() time.Time
	completed atomic.Bool
	mu        sync.Mutex
	checkedAt time.Time
}

// isCompleted caches completion forever (it is final) and an incomplete
// answer for setupRecheckInterval. Errors are never cached.
func (g *setupGate) isCompleted(ctx context.Context) (bool, error) {
	if g.completed.Load() {
		return true, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.completed.Load() {
		return true, nil
	}
	if now := g.now(); !g.checkedAt.IsZero() && now.Sub(g.checkedAt) >= 0 && now.Sub(g.checkedAt) < setupRecheckInterval {
		return false, nil
	}
	status, err := g.wizard.Status(ctx)
	if err != nil {
		return false, err
	}
	if status.Completed {
		g.completed.Store(true)
		return true, nil
	}
	g.checkedAt = g.now()
	return false, nil
}

func (g *setupGate) validToken(r *http.Request) bool {
	values := r.Header.Values(SetupTokenHeader)
	if len(g.token) == 0 || len(values) != 1 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(values[0]), g.token) == 1
}

func setupWizardPath(path string) bool {
	return path == domain.SetupAPIPrefix || strings.HasPrefix(path, domain.SetupAPIPrefix+"/")
}

// setupAllows is the gate middleware decision. It writes the refusal itself.
func (s *Server) setupAllows(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	wizard := setupWizardPath(path)
	// Probes and discovery never wait on the database.
	if !wizard && domain.SetupGateFor(false, r.Method, path) == domain.SetupGateAllow {
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
	completed, err := s.setup.isCompleted(ctx)
	cancel()
	if err != nil {
		WriteError(w, r, domain.ErrDatabase)
		return false
	}
	switch domain.SetupGateFor(completed, r.Method, path) {
	case domain.SetupGateAllow:
		return true
	case domain.SetupGateCompleted:
		writeProblem(w, r, 410, "setup_completed", "Initial setup is already complete.")
		return false
	}
	if s.frontendRequest(r) {
		return true
	}
	writeProblem(w, r, 503, "setup_required", "Initial setup has not been completed.")
	return false
}

// frontendRequest reports a GET or HEAD that no API route claims and that
// the frontend would serve, so the browser can load the wizard page itself.
func (s *Server) frontendRequest(r *http.Request) bool {
	if !s.web.handles(r) || compat.HasPrefix(r.URL.Path) || s.router == nil {
		return false
	}
	path := r.URL.RawPath
	if path == "" {
		path = r.URL.Path
	}
	// HEAD is checked as GET too: an API route registered for GET only must
	// not slip through as a frontend request and reach its 405 handler.
	return !s.router.Match(chi.NewRouteContext(), r.Method, path) && !s.router.Match(chi.NewRouteContext(), http.MethodGet, path)
}

func (s *Server) setupRoutes(r chi.Router) {
	if s.setup == nil {
		gone := func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, r, 410, "setup_completed", "Initial setup is already complete.")
		}
		r.Get(domain.SetupAPIPrefix+"/status", gone)
		r.Get(domain.SetupAPIPrefix, gone)
		r.Post(domain.SetupAPIPrefix+"/steps/{step}", gone)
		r.Post(domain.SetupAPIPrefix+"/back", gone)
		r.Post(domain.SetupAPIPrefix+"/complete", gone)
		return
	}
	r.Get(domain.SetupAPIPrefix+"/status", s.setupPublicStatus)
	r.Get(domain.SetupAPIPrefix, s.setupEndpoint(func(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (any, error) {
		status, err := s.setup.wizard.Status(ctx)
		if err != nil {
			return nil, err
		}
		if status.Completed {
			return nil, domain.ErrSetupCompleted
		}
		return status.State, nil
	}))
	r.Post(domain.SetupAPIPrefix+"/steps/{step}", s.setupEndpoint(s.setupSubmit))
	r.Post(domain.SetupAPIPrefix+"/back", s.setupEndpoint(func(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (any, error) {
		return s.setup.wizard.Back(ctx)
	}))
	r.Post(domain.SetupAPIPrefix+"/complete", s.setupEndpoint(func(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (any, error) {
		state, err := s.setup.wizard.Finish(ctx)
		if err == nil {
			s.setup.completed.Store(true)
			s.logger.Info("initial setup completed", "component", "setup")
		}
		return state, err
	}))
}

// setupPublicStatus lets the frontend decide whether to show the wizard. The
// gate answers 410 instead once setup is complete.
func (s *Server) setupPublicStatus(w http.ResponseWriter, r *http.Request) {
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"setupRequired": true, "tokenRequired": true}})
}

func (s *Server) setupEndpoint(operation func(context.Context, http.ResponseWriter, *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := strictQuery(r); err != nil {
			WriteError(w, r, err)
			return
		}
		if !s.setup.validToken(r) {
			s.logger.Warn("setup token rejected", "component", "setup", "clientIp", requestClientIP(r))
			writeProblem(w, r, 401, "setup_token_invalid", "Setup token is missing or invalid.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout())
		defer cancel()
		ctx = domain.WithSetupOrigin(ctx, domain.SetupOrigin{Channel: "http", IP: requestClientIP(r), RequestID: w.Header().Get("X-Request-ID")})
		result, err := operation(ctx, w, r)
		if err != nil {
			writeSetupError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	}
}

type setupLanguageInput struct {
	Locale string `json:"locale"`
}

type setupAdminInput struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

type setupMediaInput struct {
	Libraries []domain.SetupLibrary `json:"libraries"`
}

type setupToolchainInput struct {
	AcceptDegraded bool `json:"acceptDegraded"`
}

func (s *Server) setupSubmit(ctx context.Context, w http.ResponseWriter, r *http.Request) (any, error) {
	step, ok := domain.ParseSetupStep(chi.URLParam(r, "step"))
	if !ok || step == domain.SetupStepComplete {
		return nil, domain.ErrNotFound
	}
	decode := func(target any) error {
		return DecodeJSON(w, r, target, setupBodyLimit)
	}
	wizard := s.setup.wizard
	switch step {
	case domain.SetupStepLanguage:
		var in setupLanguageInput
		if err := decode(&in); err != nil {
			return nil, err
		}
		return wizard.SubmitLanguage(ctx, in.Locale)
	case domain.SetupStepAdmin:
		var in setupAdminInput
		if err := decode(&in); err != nil {
			return nil, err
		}
		release, admitted := s.admitAccount()
		if !admitted {
			return nil, errSetupBusy
		}
		defer release()
		return wizard.SubmitAdmin(ctx, app.SetupAdminInput{Name: in.Name, DisplayName: in.DisplayName}, in.Password)
	case domain.SetupStepDatabase:
		return wizard.SubmitDatabase(ctx)
	case domain.SetupStepMedia:
		var in setupMediaInput
		if err := decode(&in); err != nil {
			return nil, err
		}
		return wizard.SubmitMedia(ctx, in.Libraries)
	case domain.SetupStepTMDB:
		var in domain.SetupTMDB
		if err := decode(&in); err != nil {
			return nil, err
		}
		return wizard.SubmitTMDB(ctx, in)
	case domain.SetupStepToolchain:
		var in setupToolchainInput
		if err := decode(&in); err != nil {
			return nil, err
		}
		return wizard.SubmitToolchain(ctx, in.AcceptDegraded)
	case domain.SetupStepMetadataPolicy:
		var in domain.SetupMetadataPolicy
		if err := decode(&in); err != nil {
			return nil, err
		}
		return wizard.SubmitMetadataPolicy(ctx, in)
	default: // domain.SetupStepNetwork
		var in domain.SetupNetwork
		if err := decode(&in); err != nil {
			return nil, err
		}
		return wizard.SubmitNetwork(ctx, in)
	}
}

var errSetupBusy = errors.New("setup busy")

func writeSetupError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *app.SetupValidationError
	switch {
	case errors.As(err, &invalid):
		issues := invalid.Issues
		if issues == nil {
			issues = []app.SetupIssue{}
		}
		details := map[string]any{"issues": issues}
		if invalid.Step.Valid() {
			details["step"] = invalid.Step.String()
		}
		// Details carry fixed field paths and codes only, never input values.
		status, code, message := 400, "setup_validation_failed", "Setup input needs correction."
		writeProblemDetails(w, r, status, code, message, details)
	case errors.Is(err, domain.ErrSetupStepOrder):
		writeProblem(w, r, 409, "setup_step_order", "This setup step is not the current step.")
	case errors.Is(err, domain.ErrSetupCompleted):
		writeProblem(w, r, 410, "setup_completed", "Initial setup is already complete.")
	case errors.Is(err, errSetupBusy):
		writeProblem(w, r, 503, "account_busy", "Account service is busy. Try again later.")
	default:
		WriteError(w, r, err)
	}
}
