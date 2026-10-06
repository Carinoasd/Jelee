package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/go-chi/chi/v5"
)

// Access and security logs (G46.3) and context fields (G46.4).
//
// The request boundary writes one access record per request: method, the
// matched route pattern (never the path or query string), status, duration,
// response bytes, the trace ID, the authenticated user, client session and
// device, and whether the request delivered media. Authentication fills the
// identity into a per-request record the boundary reads when the request
// ends, and into the request context so every record a handler logs
// carries it.
//
// Security relevant outcomes are written once more under the mandatory
// "security" scope: failed logins and account locks noted by storage,
// throttled logins, blocked clients, denied access (403), CSRF failures,
// refused webhook targets (SSRF) and rejected setup tokens. Developer mode
// changes log themselves under the security scope (component devmode).

// registerRoutePatterns lets the access log name every route this handler
// serves, including those of mounted routers (the compatibility layer).
func registerRoutePatterns(r chi.Routes) {
	var patterns []string
	_ = chi.Walk(r, func(_ string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		patterns = append(patterns, route)
		return nil
	})
	logging.RegisterRoutePatterns(patterns...)
}

// requestRecord is what the boundary learns about a request while it runs.
type requestRecord struct {
	mu       sync.Mutex
	userID   string
	clientID string
	deviceID string
	code     string
}

type requestRecordKey struct{}

func requestRecordFrom(ctx context.Context) *requestRecord {
	rec, _ := ctx.Value(requestRecordKey{}).(*requestRecord)
	return rec
}

func (rec *requestRecord) setIdentity(f domain.LogFields) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.userID, rec.clientID, rec.deviceID = f.UserID, f.ClientID, f.DeviceID
}

func (rec *requestRecord) setCode(errorCode string) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.code == "" {
		rec.code = errorCode
	}
}

func (rec *requestRecord) snapshot() (userID, clientID, deviceID, errorCode string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.userID, rec.clientID, rec.deviceID, rec.code
}

// identityFields are the G46.4 fields of an authenticated principal.
func identityFields(p access.Principal, deviceID string) domain.LogFields {
	return domain.LogFields{UserID: p.UserID, ClientID: p.SessionID, DeviceID: deviceID}
}

// routeFields names the item, library or job a matched route works on, from
// its path parameters. Only identifiers are taken; the log router validates
// them again.
func routeFields(r *http.Request) domain.LogFields {
	var f domain.LogFields
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return f
	}
	pattern := rctx.RoutePattern()
	for i, key := range rctx.URLParams.Keys {
		if i >= len(rctx.URLParams.Values) {
			break
		}
		value := rctx.URLParams.Values[i]
		if !domain.ValidID(value) {
			continue
		}
		switch {
		case key == "itemId", key == "id" && strings.HasPrefix(pattern, "/api/v1/items/"):
			f.ItemID = value
		case key == "libraryId", key == "id" && strings.HasPrefix(pattern, "/api/v1/libraries/"):
			f.LibraryID = value
		case key == "id" && strings.HasPrefix(pattern, "/api/v1/jobs/"):
			f.TaskID = value
		}
	}
	return f
}

// identityContext records an authenticated principal for the access log
// and returns ctx (the request's context) with the G46.4 fields of the
// principal and of r's matched route.
func identityContext(ctx context.Context, r *http.Request, p access.Principal, deviceID string) context.Context {
	identity := identityFields(p, deviceID)
	requestRecordFrom(ctx).setIdentity(identity)
	route := routeFields(r)
	identity.ItemID, identity.LibraryID, identity.TaskID = route.ItemID, route.LibraryID, route.TaskID
	return domain.WithLogFields(ctx, identity)
}

// recordIdentity records a principal authenticated outside the native
// middleware (the compatibility layer) for the access log.
func recordIdentity(ctx context.Context, p access.Principal) {
	if p.UserID != "" {
		requestRecordFrom(ctx).setIdentity(identityFields(p, ""))
	}
}

// accessWriter counts the status and body bytes of a response. It keeps
// net/http's zero-copy path (ReadFrom) and lets http.ResponseController
// reach the connection through Unwrap.
type accessWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *accessWriter) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *accessWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *accessWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	var n int64
	var err error
	if to, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = to.ReadFrom(src)
	} else {
		n, err = io.Copy(writerOnly{w.ResponseWriter}, src)
	}
	w.bytes += n
	return n, err
}

func (w *accessWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// mediaRoute reports a route that delivers media: direct streams, tracks,
// extracted subtitles and attachments, and the compatibility layer's
// video, audio and subtitle routes.
func mediaRoute(pattern string) bool {
	if strings.HasPrefix(pattern, "/api/v1/sources/") {
		return true
	}
	if !strings.HasPrefix(pattern, "/compat") {
		return false
	}
	lower := strings.ToLower(pattern)
	return strings.Contains(lower, "/videos/") || strings.Contains(lower, "/audio/")
}

// loginRoute reports the password and second factor login routes.
func loginRoute(pattern string) bool {
	return strings.HasPrefix(pattern, "/api/v1/auth/login") || strings.HasSuffix(strings.ToLower(pattern), "/authenticatebyname") || pattern == "/api/v1/shares/redeem" || pattern == "/api/v1/shares/redeem/native"
}

// securityCodes maps error codes to security log events.
var securityCodes = map[string]string{ //nolint:gosec // G101: error code and event names, not credentials
	"auth_rate_limited":       "login_throttled",
	"client_blocked":          "client_blocked",
	"client_pending_approval": "client_pending",
	"client_rate_limited":     "client_rate_limited",
	"forbidden":               "access_denied",
	"share_forbidden":         "access_denied",
	"csrf_failed":             "csrf_failed",
	"webhook_target_denied":   "ssrf_blocked",
	"setup_token_invalid":     "setup_token_rejected",
}

// securityEvents lists the events of one finished request: those storage
// noted, then the one its error code implies, then a failed login that
// noted nothing (an unknown account).
func securityEvents(pattern string, status int, errorCode string, notes []string) []string {
	events := notes
	if event, ok := securityCodes[errorCode]; ok {
		events = append(events, event)
	}
	if len(notes) == 0 && status == http.StatusUnauthorized && loginRoute(pattern) {
		events = append(events, "login_failed")
	}
	return events
}

// securityLimiter bounds the security records of one event to a burst per
// second, so an attack cannot flood the log; the next record written
// reports how many were suppressed.
type securityLimiter struct {
	mu     sync.Mutex
	window time.Time
	counts map[string]int
	held   map[string]int64
}

const securityBurst = 20

func (l *securityLimiter) allow(event string, now time.Time) (bool, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil || now.Sub(l.window) >= time.Second {
		l.window, l.counts = now, map[string]int{}
	}
	if l.held == nil {
		l.held = map[string]int64{}
	}
	if l.counts[event] >= securityBurst {
		l.held[event]++
		return false, 0
	}
	l.counts[event]++
	suppressed := l.held[event]
	delete(l.held, event)
	return true, suppressed
}

// logRequest writes the access record and any security records of a
// finished request.
func (s *Server) logRequest(ctx context.Context, r *http.Request, w *accessWriter, rec *requestRecord, notes *domain.SecurityNotes, started time.Time) {
	pattern := ""
	if rctx := chi.RouteContext(ctx); rctx != nil {
		pattern = rctx.RoutePattern()
	}
	route := pattern
	if route == "" {
		route = "unmatched"
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	userID, clientID, deviceID, errorCode := rec.snapshot()
	attrs := make([]any, 0, 26)
	attrs = append(attrs, "component", "http", "requestId", w.Header().Get("X-Request-ID"), "method", logging.SafeMethod(r.Method), "route", route,
		"status", status, "durationMs", time.Since(started).Milliseconds(), "bytes", w.bytes, "media", mediaRoute(pattern))
	identity := make([]any, 0, 8)
	for _, field := range [...]struct{ key, value string }{{"userId", userID}, {"clientId", clientID}, {"deviceId", deviceID}} {
		if field.value != "" {
			identity = append(identity, field.key, field.value)
		}
	}
	attrs = append(attrs, identity...)
	s.logger.InfoContext(ctx, "request completed", attrs...)
	events := securityEvents(pattern, status, errorCode, notes.Events())
	if len(events) == 0 {
		return
	}
	domain.ForceTraceSampling(ctx)
	for _, event := range events {
		ok, suppressed := s.securityLog.allow(event, time.Now())
		if !ok {
			continue
		}
		record := append([]any{"component", "security", "event", event, "requestId", w.Header().Get("X-Request-ID"), "method", logging.SafeMethod(r.Method), "route", route, "status", status}, identity...)
		if suppressed > 0 {
			record = append(record, "suppressed", suppressed)
		}
		s.logger.WarnContext(ctx, "security event", record...)
	}
}

// logPanic archives a recovered panic: the value's type and the masked
// stack, never the value itself (it may hold request data).
func (s *Server) logPanic(ctx context.Context, w http.ResponseWriter, recovered any) {
	route := "unmatched"
	if rctx := chi.RouteContext(ctx); rctx != nil && rctx.RoutePattern() != "" {
		route = rctx.RoutePattern()
	}
	s.logger.ErrorContext(ctx, "request panic", "component", "http", "requestId", w.Header().Get("X-Request-ID"), "route", route,
		"panicType", fmt.Sprintf("%T", recovered), "stack", logging.PanicStack(debug.Stack()))
}
