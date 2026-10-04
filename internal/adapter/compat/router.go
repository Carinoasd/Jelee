package compat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Prefix is where the compatibility layer is mounted. Clients are configured
// with "https://host/compat" as their server address.
const Prefix = "/compat"

// genericError is the only error text the layer ever writes, matching the
// upstream production exception response; it carries no internal detail.
const genericError = "Error processing request."

// Authenticator resolves a syntactically valid session token. It must be the
// server's own session lookup (expiry, revocation, disabled and deleted users),
// never a second implementation.
type Authenticator func(context.Context, string) (access.Principal, error)

// Options configures NewRouter. Every field is required.
type Options struct {
	Authenticate Authenticator
	// WriteRejection writes the server's own error envelope for a request to
	// transform media (G10.3: one global transcode_disabled code and status).
	WriteRejection func(http.ResponseWriter, *http.Request, error)
	// ServerID is the stable server identifier: 32 lowercase hex digits.
	ServerID string
	// Timeout bounds each session lookup.
	Timeout time.Duration
}

type router struct {
	opts Options
	mux  *chi.Mux
	// patterns holds every registered route split into path segments; it is
	// fixed once NewRouter returns.
	patterns [][]string
}

// NewRouter builds the compatibility router. Mount it at Prefix behind the
// server boundary (Host check, path transformation guard, removed features).
// The returned mux routes on the path below Prefix; it derives that path from
// the request URL itself, so it also serves requests whose prefix differs
// only in letter case and were not matched by an exact mount.
func NewRouter(opts Options) (*chi.Mux, error) {
	if opts.Authenticate == nil || opts.WriteRejection == nil || opts.Timeout <= 0 {
		return nil, errors.New("compat: authenticator, rejection writer and timeout are required")
	}
	if !validServerID(opts.ServerID) {
		return nil, errors.New("compat: server identifier must be 32 lowercase hex digits")
	}
	rt := &router{opts: opts, mux: chi.NewRouter()}
	rt.mux.Use(rt.boundary)
	rt.mux.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeError(w, http.StatusNotFound) })
	rt.mux.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) { writeError(w, http.StatusMethodNotAllowed) })
	rt.systemRoutes()
	return rt.mux, nil
}

// HasPrefix reports whether path is Prefix or lies below it, comparing the
// prefix case-insensitively like the rest of the layer.
func HasPrefix(path string) bool {
	_, ok := trimPrefix(path)
	return ok
}

func trimPrefix(path string) (string, bool) {
	if len(path) < len(Prefix) || !strings.EqualFold(path[:len(Prefix)], Prefix) {
		return "", false
	}
	rest := path[len(Prefix):]
	if rest != "" && rest[0] != '/' {
		return "", false
	}
	return rest, true
}

// DeriveServerID returns a stable identifier from non-secret configuration
// values. The order of values does not matter. Only a one-way digest is
// published, never the values themselves.
func DeriveServerID(values ...string) string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte("jelee-compat-server-id-v1\x00" + strings.Join(sorted, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func validServerID(id string) bool {
	if len(id) != 32 || isNilID(id) {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// handle registers a route. Patterns use the upstream spelling; matching is
// case-insensitive (see canonical). Authenticated routes require a native
// session.
func (rt *router) handle(method, pattern string, authenticated bool, h http.HandlerFunc) {
	rt.patterns = append(rt.patterns, strings.Split(pattern, "/"))
	if authenticated {
		rt.mux.With(rt.authenticate).Method(method, pattern, h)
		return
	}
	rt.mux.Method(method, pattern, h)
}

// boundary applies the layer-wide rules before routing, in this order:
//  1. any request carrying an Origin header is refused with 403, so no
//     browser page (cross-origin or same-origin) can use native credentials
//     through this layer; no CORS response header is ever sent, which also
//     answers preflight requests with a refusal;
//  2. the production transformation guard inspects path, query and body;
//  3. the path below Prefix is matched case-insensitively and rewritten to
//     the registered spelling for the router.
func (rt *router) boundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("Origin")) > 0 {
			writeError(w, http.StatusForbidden)
			return
		}
		if err := media.GuardProduction(r); err != nil {
			switch {
			case errors.Is(err, media.ErrTranscodeDisabled):
				rt.opts.WriteRejection(w, r, err)
			case errors.Is(err, media.ErrBodyTooLarge):
				writeError(w, http.StatusRequestEntityTooLarge)
			case errors.Is(err, media.ErrUnsupportedMediaType):
				writeError(w, http.StatusUnsupportedMediaType)
			default:
				writeError(w, http.StatusBadRequest)
			}
			return
		}
		rest, ok := trimPrefix(r.URL.Path)
		// An encoded slash would let one segment pose as two (or hide one);
		// upstream routing never matches it either.
		if !ok || strings.Contains(strings.ToLower(r.URL.RawPath), "%2f") {
			writeError(w, http.StatusNotFound)
			return
		}
		rctx := chi.RouteContext(r.Context())
		if rctx == nil {
			writeError(w, http.StatusNotFound)
			return
		}
		rctx.RoutePath = rt.canonical(rest)
		next.ServeHTTP(w, r)
	})
}

// canonical rewrites the literal segments of the best matching registered
// pattern to their registered spelling. Like upstream routing, literal
// segments compare case-insensitively, one trailing slash is ignored, and a
// literal match beats a parameter (the pattern with most literal segments
// wins). Parameter segments are passed through unchanged. Without a match the
// path is returned as is and the router answers 404.
func (rt *router) canonical(rest string) string {
	if rest == "" {
		return "/"
	}
	if len(rest) > 1 && strings.HasSuffix(rest, "/") {
		rest = rest[:len(rest)-1]
	}
	segments := strings.Split(rest, "/")
	var best []string
	bestLiterals := -1
	for _, pattern := range rt.patterns {
		if len(pattern) != len(segments) {
			continue
		}
		literals, match := 0, true
		for i := 1; i < len(pattern) && match; i++ {
			if strings.HasPrefix(pattern[i], "{") {
				match = segments[i] != ""
				continue
			}
			match = strings.EqualFold(pattern[i], segments[i])
			literals++
		}
		if match && literals > bestLiterals {
			best, bestLiterals = pattern, literals
		}
	}
	if best == nil {
		return rest
	}
	out := append([]string(nil), segments...)
	for i := 1; i < len(best); i++ {
		if !strings.HasPrefix(best[i], "{") {
			out[i] = best[i]
		}
	}
	return strings.Join(out, "/")
}

// authenticate accepts only native sessions. A web session token is refused
// with 401 exactly like an unknown token: upstream answers every credential it
// cannot use with an authentication challenge, and an identical answer keeps
// the layer from confirming that a token is a live web session. Cookies are
// never consulted, so a browser session cannot ride along either.
func (rt *router) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, err := ParseClientAuth(r.Header, r.URL.Query())
		if err != nil || client.Token == "" {
			writeError(w, http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), rt.opts.Timeout)
		principal, err := rt.opts.Authenticate(ctx, client.Token)
		cancel()
		switch {
		case errors.Is(err, domain.ErrUnauthenticated):
			writeError(w, http.StatusUnauthorized)
			return
		case errors.Is(err, domain.ErrDatabase):
			writeError(w, http.StatusServiceUnavailable)
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError)
			return
		case principal.Kind != access.ClientNative || principal.UserID == "" || principal.SessionID == "":
			writeError(w, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(access.WithPrincipal(r.Context(), principal)))
	})
}

// writeError is the single error writer of the layer. Like the upstream
// framework, authentication, authorization, routing and availability failures
// carry no body; request and server failures carry only the generic text.
func writeError(w http.ResponseWriter, status int) {
	h := w.Header()
	h.Del("Content-Type")
	if status == http.StatusBadRequest || status == http.StatusInternalServerError {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Content-Length", strconv.Itoa(len(genericError)))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, genericError)
		return
	}
	h.Set("Content-Length", "0")
	w.WriteHeader(status)
}

// writeJSON writes value in the upstream default representation: PascalCase
// keys from the struct tags, null members omitted, no trailing newline.
func writeJSON(w http.ResponseWriter, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
