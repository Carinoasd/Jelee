package httpapi

import (
	"errors"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/go-chi/chi/v5"
)

// Developer mode routes (G45). They exist only on an instance that meets its
// own thresholds (JELEE_DEV_MODE=true and dev.enabled, outside production);
// everywhere else they are not registered and answer 404 like any unknown
// path. Enabling is never possible over HTTP: the loopback-only token route
// hands out a one-time token that only `jelee-cli devmode enable` redeems.

const (
	// DevAPIPrefix holds every developer mode API route.
	DevAPIPrefix = "/api/v1/dev"
	// devPprofPrefix is the only /debug/ path a capable instance routes.
	devPprofPrefix = "/debug/pprof/"
)

var (
	errDevInactive          = errors.New("devmode_inactive")
	errDevToggleUnavailable = errors.New("devmode_toggle_unavailable")
	// errConfirmationRequired refuses a dangerous operation sent without
	// its explicit acknowledgement (G45.6).
	errConfirmationRequired = errors.New("confirmation_required")
)

// WithDevMode attaches the developer mode controller. It takes effect only
// when the configuration meets the local thresholds; a capable configuration
// without a controller is refused.
func WithDevMode(c *devmode.Controller) Option { return func(s *Server) { s.dev = c } }

func (s *Server) configureDevMode() error {
	if !s.cfg.Dev.Capable() {
		// Production and every incomplete configuration: no routes, no
		// relaxations, whatever was passed.
		s.dev = nil
		return nil
	}
	switch {
	case !s.cfg.EnableAccounts:
		return errors.New("developer mode needs the account service for its administrator routes")
	case s.dev == nil:
		return errors.New("developer mode configuration needs its controller")
	case !s.dev.Capable():
		return errors.New("developer mode controller disagrees with the configuration")
	}
	if s.clients != nil {
		s.clients.dev = s.dev
	}
	return nil
}

func (s *Server) devStatus() devmode.Status {
	if s.dev == nil {
		return devmode.Status{}
	}
	return s.dev.Status()
}

func (s *Server) devRoutes(r chi.Router) {
	r.Post(DevAPIPrefix+"/token", s.devToken)
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate)
		r.Get(DevAPIPrefix, s.accountEndpoint(true, false, s.devState))
		r.Put(DevAPIPrefix+"/toggles/{toggle}", s.accountEndpoint(true, false, s.devSetToggle))
		r.Post(DevAPIPrefix+"/disable", s.accountEndpoint(true, false, s.devDisable))
	})
	r.Get(devPprofPrefix+"*", s.devPprof)
	r.Post(devPprofPrefix+"symbol", s.devPprof)
}

// devLoopback reports a request whose transport peer is loopback and that
// carries no forwarding header: a reverse proxy on the same host would
// otherwise turn every remote client into loopback (G45.2).
func devLoopback(r *http.Request) bool {
	addr, err := netip.ParseAddr(ClientIP(r))
	return err == nil && addr.Unmap().IsLoopback() && !forwardingHeadersPresent(r)
}

func devActor(a domain.Actor) devmode.Actor {
	return devmode.Actor{UserID: a.UserID, SessionID: a.SessionID, IP: a.IP}
}

// devToken issues a one-time enable token to a loopback caller. Any other
// caller gets 404, so the entry is invisible off the host.
func (s *Server) devToken(w http.ResponseWriter, r *http.Request) {
	if !devLoopback(r) {
		WriteError(w, r, domain.ErrNotFound)
		return
	}
	if _, err := strictQuery(r); err != nil {
		WriteError(w, r, err)
		return
	}
	if err := emptyAccountInput(w, r); err != nil {
		WriteError(w, r, err)
		return
	}
	token, expires, err := s.dev.IssueToken(r.Context(), devmode.Actor{IP: ClientIP(r)}, true)
	if err != nil {
		WriteError(w, r, devError(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"token": token, "expiresAt": expires.UTC().Format(time.RFC3339),
		"enable": "jelee-cli devmode enable --token <token>"}})
}

type devToggleView struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Dangerous bool   `json:"dangerous"`
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
}

type devStateView struct {
	Active     bool            `json:"active"`
	EnabledAt  *time.Time      `json:"enabledAt,omitempty"`
	ExpiresAt  *time.Time      `json:"expiresAt,omitempty"`
	Source     string          `json:"source,omitempty"`
	TTLSeconds int64           `json:"ttlSeconds"`
	Toggles    []devToggleView `json:"toggles"`
}

func (s *Server) devView(st devmode.Status) devStateView {
	v := devStateView{Active: st.Active, Source: st.Source, TTLSeconds: int64(s.dev.TTL() / time.Second), Toggles: []devToggleView{}}
	if st.Active {
		enabled, expires := st.EnabledAt.UTC(), st.ExpiresAt.UTC()
		v.EnabledAt, v.ExpiresAt = &enabled, &expires
	}
	for _, t := range devmode.Toggles() {
		kind := "restriction"
		if t.Kind() == devmode.KindDebug {
			kind = "debug"
		}
		on := false
		for _, e := range st.Toggles {
			on = on || e == t
		}
		v.Toggles = append(v.Toggles, devToggleView{Name: string(t), Kind: kind, Dangerous: t.Dangerous(), Available: s.dev.Available(t), Enabled: on})
	}
	return v
}

func (s *Server) devState(w http.ResponseWriter, r *http.Request, _ domain.Actor) (any, int, error) {
	return s.devView(s.dev.Status()), http.StatusOK, nil
}

func (s *Server) devSetToggle(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input struct {
		Enabled     *bool `json:"enabled"`
		IUnderstand bool  `json:"iUnderstand"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	if input.Enabled == nil {
		return nil, 0, domain.ErrInvalid
	}
	st, err := s.dev.SetToggle(r.Context(), devActor(a), devmode.Toggle(chi.URLParam(r, "toggle")), *input.Enabled, devmode.Confirmation{IUnderstand: input.IUnderstand}, "api")
	if err != nil {
		return nil, 0, devError(err)
	}
	return s.devView(st), http.StatusOK, nil
}

func (s *Server) devDisable(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	if err := emptyAccountInput(w, r); err != nil {
		return nil, 0, err
	}
	if err := s.dev.Disable(r.Context(), devActor(a), "api", "disabled by an administrator"); err != nil {
		return nil, 0, devError(err)
	}
	return nil, http.StatusNoContent, nil
}

// devPprof serves net/http/pprof while the debug_pprof toggle is on, to
// loopback callers and administrators only. Everything else is 404.
func (s *Server) devPprof(w http.ResponseWriter, r *http.Request) {
	if !s.dev.Effective(devmode.DebugPprof) {
		WriteError(w, r, domain.ErrNotFound)
		return
	}
	if devLoopback(r) {
		servePprof(w, r)
		return
	}
	s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := access.PrincipalFromContext(r.Context()); !p.Admin {
			WriteError(w, r, domain.ErrNotFound)
			return
		}
		servePprof(w, r)
	})).ServeHTTP(w, r)
}

func servePprof(w http.ResponseWriter, r *http.Request) {
	// pprof writes its own content types; the API's restrictive CSP stays.
	w.Header().Del("Content-Type")
	switch strings.TrimPrefix(r.URL.Path, devPprofPrefix) {
	case "cmdline":
		pprof.Cmdline(w, r)
	case "profile":
		pprof.Profile(w, r)
	case "symbol":
		pprof.Symbol(w, r)
	case "trace":
		pprof.Trace(w, r)
	default:
		pprof.Index(w, r)
	}
}

// devError maps controller errors onto the HTTP error table.
func devError(err error) error {
	switch {
	case errors.Is(err, devmode.ErrUnknownToggle), errors.Is(err, devmode.ErrNotLoopback), errors.Is(err, devmode.ErrDenied), errors.Is(err, devmode.ErrProduction):
		return domain.ErrNotFound
	case errors.Is(err, devmode.ErrInactive):
		return errDevInactive
	case errors.Is(err, devmode.ErrToggleUnavailable):
		return errDevToggleUnavailable
	case errors.Is(err, devmode.ErrConfirmationNeeded):
		return errConfirmationRequired
	}
	return err
}
