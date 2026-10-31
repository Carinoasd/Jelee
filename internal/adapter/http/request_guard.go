package httpapi

import (
	"container/list"
	"context"
	"errors"
	"hash/maphash"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/compat"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// Request guards (G08.1, G08.7). After the boundary (request ID, panic
// recovery, access log, root span) every request passes, in this order:
//
//  1. requestTimeout: one processing deadline for the whole request, carried
//     by the request context into storage and external programs;
//  2. requestBodyLimit: one ceiling on the request body;
//  3. rateLimitGate: the per-client token bucket of anonymous requests;
//     requests that carry a credential are charged after authentication
//     instead, against their user and session token (chargePrincipal).
//
// Direct media delivery, item images, subtitle and attachment tracks and
// playback information (native and compatibility routes) bypass all three:
// a player's Range requests are never refused, cut or capped by them.

// RequestGuardStats counts what the request guards refused since the process
// started. It only reads memory; the zero value is ready and a nil pointer
// reads zero.
type RequestGuardStats struct {
	rateLimited atomic.Int64
	timedOut    atomic.Int64
}

// NewRequestGuardStats returns empty counters for WithRequestGuardStats.
func NewRequestGuardStats() *RequestGuardStats { return &RequestGuardStats{} }

// RateLimitedTotal is the number of requests refused with 429 rate_limited.
func (g *RequestGuardStats) RateLimitedTotal() int64 {
	if g == nil {
		return 0
	}
	return g.rateLimited.Load()
}

// TimeoutsTotal is the number of requests answered 504 processing_timeout.
func (g *RequestGuardStats) TimeoutsTotal() int64 {
	if g == nil {
		return 0
	}
	return g.timedOut.Load()
}

// WithRequestGuardStats lets the caller export the guard counters (metrics).
func WithRequestGuardStats(stats *RequestGuardStats) Option {
	return func(s *Server) { s.guardStats = stats }
}

// errProcessingTimeout is the cause of the request deadline set by
// requestTimeout; deadlines of individual lookups have other causes.
var errProcessingTimeout = errors.New("request processing timed out")

// guardPolicy is what the guards apply to one request.
type guardPolicy struct {
	rate    bool
	timeout bool
	// body is the request body ceiling; 0 applies none.
	body int64
}

// guardPolicy classifies a request by its path before routing.
func (s *Server) guardPolicy(r *http.Request) guardPolicy {
	path := r.URL.Path
	if deliveryPath(path) {
		return guardPolicy{}
	}
	p := guardPolicy{rate: strings.HasPrefix(path, "/api/") || compat.HasPrefix(path), timeout: true, body: s.cfg.API.BodyLimit()}
	if longRunningPath(path) {
		p.timeout = false
	}
	if r.Method == http.MethodPost && itemImageUploadPath(path) {
		// The upload handler owns its ceiling (images.maxSourceBytes) and
		// answers 413 image_too_large itself.
		p.body = 0
	}
	return p
}

// deliveryPath reports the routes that deliver media or prepare playback.
func deliveryPath(path string) bool {
	if strings.HasPrefix(path, "/api/v1/sources/") || strings.HasPrefix(path, "/images/") {
		return true
	}
	if compat.HasPrefix(path) {
		return compat.IsDeliveryPath(path)
	}
	// /api/v1/items/{id}/playback and /api/v1/items/{id}/playback/check
	p := splitPath(path)
	return (p.n == 6 || p.n == 7) && p.parts[0] == "" && p.parts[1] == "api" && p.parts[2] == "v1" && p.parts[3] == "items" &&
		p.parts[4] != "" && p.parts[5] == "playback" && (p.n == 6 || p.parts[6] == "check")
}

// pathParts are the "/"-separated parts of a request path, split without
// allocating. A path with more parts than the array holds reports n as 0,
// so it matches none of the route shapes above.
type pathParts struct {
	parts [8]string
	n     int
}

func splitPath(path string) pathParts {
	var p pathParts
	for {
		part, rest, more := strings.Cut(path, "/")
		if p.n == len(p.parts) {
			return pathParts{}
		}
		p.parts[p.n] = part
		p.n++
		if !more {
			return p
		}
		path = rest
	}
}

// longRunningPath reports the downloads that set their own, longer
// deadlines (personal data and watch statistics exports) and the profiler.
func longRunningPath(path string) bool {
	if path == "/api/v1/watch-stats/export" || strings.HasPrefix(path, devPprofPrefix) {
		return true
	}
	p := splitPath(path)
	return p.n == 6 && p.parts[0] == "" && p.parts[1] == "api" && p.parts[2] == "v1" && p.parts[3] == "users" && p.parts[4] != "" && p.parts[5] == "data-export"
}

// itemImageUploadPath is /api/v1/items/{id}/images/{type}.
func itemImageUploadPath(path string) bool {
	p := splitPath(path)
	return p.n == 7 && p.parts[0] == "" && p.parts[1] == "api" && p.parts[2] == "v1" && p.parts[3] == "items" && p.parts[4] != "" && p.parts[5] == "images" && p.parts[6] != ""
}

// requestTimeout bounds the processing of one request. The deadline reaches
// every lookup, query and external program through the request context.
// When it expires before the handler sent its status, the handler's answer
// (usually 408 from the cancelled work) is replaced by 504
// processing_timeout and the timeout is logged and counted.
func (s *Server) requestTimeout(next http.Handler) http.Handler {
	timeout := s.cfg.API.Timeout(s.cfg.RequestTimeoutSeconds)
	if s.guardTimeout > 0 {
		timeout = s.guardTimeout
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.guardPolicy(r).timeout {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeoutCause(r.Context(), timeout, errProcessingTimeout)
		defer cancel()
		r = r.WithContext(ctx)
		tw := &timeoutWriter{ResponseWriter: w, r: r}
		next.ServeHTTP(tw, r)
		if !tw.wrote && expired(ctx) {
			tw.expire() //nolint:contextcheck // expire answers with the request (and context) the writer was built for
		}
		if tw.timedOut {
			s.guardStats.timedOut.Add(1)
			s.logger.WarnContext(ctx, "request processing timed out", "component", "http", "method", r.Method, "timeoutMs", timeout.Milliseconds())
		}
	})
}

func expired(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errProcessingTimeout)
}

// timeoutWriter replaces the response of a request whose processing deadline
// expired before its status was sent. It is used by one goroutine only.
type timeoutWriter struct {
	http.ResponseWriter
	r        *http.Request
	wrote    bool
	timedOut bool
}

func (w *timeoutWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *timeoutWriter) WriteHeader(status int) {
	if w.wrote {
		if !w.timedOut {
			w.ResponseWriter.WriteHeader(status)
		}
		return
	}
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.wrote = true
	if expired(w.r.Context()) {
		w.expire()
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *timeoutWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.timedOut {
		return 0, http.ErrHandlerTimeout
	}
	return w.ResponseWriter.Write(b)
}

// expire sends 504 processing_timeout in place of the handler's response.
func (w *timeoutWriter) expire() {
	w.wrote, w.timedOut = true, true
	h := w.ResponseWriter.Header()
	for _, name := range []string{"Content-Length", "Content-Disposition", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified", "Trailer"} {
		h.Del(name)
	}
	if compat.HasPrefix(w.r.URL.Path) {
		requestRecordFrom(w.r.Context()).setCode("processing_timeout")
		compat.WriteStatus(w.ResponseWriter, http.StatusGatewayTimeout)
		return
	}
	writeProblem(w.ResponseWriter, w.r, 504, "processing_timeout", "Request processing exceeded the server time limit.")
}

// requestBodyLimit applies the request body ceiling. A declared length over
// it is refused at once; an undeclared one stops reading at the ceiling and
// the decoder answers 413 body_too_large like for a route's own limit.
func (s *Server) requestBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := s.guardPolicy(r).body
		if limit > 0 && r.Body != nil && r.Body != http.NoBody {
			if r.ContentLength > limit {
				if compat.HasPrefix(r.URL.Path) {
					requestRecordFrom(r.Context()).setCode("body_too_large")
					compat.WriteStatus(w, http.StatusRequestEntityTooLarge)
					return
				}
				WriteError(w, r, media.ErrBodyTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// rateCharge carries the deferred charge of a request with a credential from
// rateLimitGate to the authentication stage.
type rateCharge struct {
	header  http.Header
	charged bool
}

type rateChargeKey struct{}

// rateLimitGate charges anonymous API requests to the bucket of their client
// address (from the trusted proxy rules, never from a header alone). A
// request with a credential is charged after authentication to its user and
// token instead; when it was not (authentication failed, or the route does
// not authenticate) it is charged to its address afterwards, and an address
// whose bucket is empty is refused before any credential lookup, so failed
// authentications cannot be repeated without limit.
func (s *Server) rateLimitGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.rateLimiter == nil || !s.guardPolicy(r).rate {
			next.ServeHTTP(w, r)
			return
		}
		key := "i:" + requestClientIP(r)
		if !carriesCredential(r) {
			d := s.rateLimiter.take(key)
			s.rateLimiter.setHeaders(w.Header(), d)
			if !d.allowed {
				s.rejectRate(w, r, d.retry)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if d := s.rateLimiter.peek(key); !d.allowed {
			s.rateLimiter.setHeaders(w.Header(), d)
			s.rejectRate(w, r, d.retry)
			return
		}
		charge := &rateCharge{header: w.Header()}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), rateChargeKey{}, charge)))
		if !charge.charged {
			s.rateLimiter.drain(key)
		}
	})
}

// carriesCredential reports whether the request presents a session token the
// route may authenticate: a bearer header or the session cookie natively,
// any credential the layer reads below the compatibility prefix.
func carriesCredential(r *http.Request) bool {
	if compat.HasPrefix(r.URL.Path) {
		auth, err := compat.ParseClientAuth(r.Header, r.URL.Query())
		return err != nil || auth.Token != ""
	}
	if len(r.Header.Values("Authorization")) > 0 {
		return true
	}
	_, present := sessionCookieToken(r)
	return present
}

// rateLimitedError refuses a request over its bucket; RetryAfter is when one
// request fits again.
type rateLimitedError struct{ retry time.Duration }

func (e rateLimitedError) Error() string             { return domain.ErrRateLimited.Error() }
func (e rateLimitedError) Unwrap() error             { return domain.ErrRateLimited }
func (e rateLimitedError) RetryAfter() time.Duration { return e.retry }

// chargePrincipal charges an authenticated request to the bucket of its user
// and session token, once per request, right after authentication. It
// returns a domain.ErrRateLimited error when the bucket is empty. Requests
// the gate exempted (or a disabled limit) carry no charge and pass.
func (s *Server) chargePrincipal(ctx context.Context, p access.Principal, token string) error {
	charge, _ := ctx.Value(rateChargeKey{}).(*rateCharge)
	if charge == nil || charge.charged || s.rateLimiter == nil {
		return nil
	}
	charge.charged = true
	// The token is kept only as a process-local digest.
	key := "p:" + p.UserID + ":" + strconv.FormatUint(maphash.String(s.rateLimiter.seed, token), 16)
	d := s.rateLimiter.take(key)
	s.rateLimiter.setHeaders(charge.header, d)
	if !d.allowed {
		s.guardStats.rateLimited.Add(1)
		return rateLimitedError{retry: d.retry}
	}
	return nil
}

func (s *Server) rejectRate(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	s.guardStats.rateLimited.Add(1)
	if compat.HasPrefix(r.URL.Path) {
		w.Header().Set("Retry-After", strconv.FormatInt(retrySeconds(retry), 10))
		requestRecordFrom(r.Context()).setCode("rate_limited")
		compat.WriteStatus(w, http.StatusTooManyRequests)
		return
	}
	WriteError(w, r, rateLimitedError{retry: retry})
}

// retrySeconds rounds a wait up to whole seconds, at least one.
func retrySeconds(d time.Duration) int64 {
	if n := int64((d + time.Second - 1) / time.Second); n > 1 {
		return n
	}
	return 1
}

// apiRateLimiter holds one token bucket per client key in memory, bounded
// to max keys with the least recently used evicted first. It is
// process-local: every instance limits its own traffic.
type apiRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	max     int
	now     func() time.Time
	seed    maphash.Seed
	entries map[string]*list.Element
	order   *list.List
	policy  string
}

type rateBucket struct {
	key    string
	tokens float64
	last   time.Time
}

type rateDecision struct {
	allowed   bool
	remaining int
	// reset is when the bucket is full again, retry when one request fits.
	reset, retry time.Duration
}

func newAPIRateLimiter(rate, burst, maxKeys int, now func() time.Time) *apiRateLimiter {
	if now == nil {
		now = time.Now
	}
	window := int64(math.Ceil(float64(burst) / float64(rate)))
	return &apiRateLimiter{rate: float64(rate), burst: float64(burst), max: maxKeys, now: now, seed: maphash.MakeSeed(),
		entries: make(map[string]*list.Element), order: list.New(), policy: strconv.Itoa(burst) + ";w=" + strconv.FormatInt(window, 10)}
}

// bucket returns the refilled bucket of key, creating a full one. The caller
// holds mu.
func (l *apiRateLimiter) bucket(key string, now time.Time) *rateBucket {
	if e, ok := l.entries[key]; ok {
		l.order.MoveToFront(e)
		b := e.Value.(*rateBucket)
		if elapsed := now.Sub(b.last); elapsed > 0 {
			b.tokens = math.Min(l.burst, b.tokens+elapsed.Seconds()*l.rate)
		}
		b.last = now
		return b
	}
	for l.order.Len() >= l.max {
		oldest := l.order.Back()
		l.order.Remove(oldest)
		delete(l.entries, oldest.Value.(*rateBucket).key)
	}
	b := &rateBucket{key: key, tokens: l.burst, last: now}
	l.entries[key] = l.order.PushFront(b)
	return b
}

func (l *apiRateLimiter) decision(b *rateBucket, allowed bool) rateDecision {
	d := rateDecision{allowed: allowed, remaining: int(math.Floor(b.tokens)), reset: l.wait(l.burst - b.tokens)}
	if !allowed {
		d.retry = l.wait(1 - b.tokens)
	}
	return d
}

func (l *apiRateLimiter) wait(tokens float64) time.Duration {
	if tokens <= 0 {
		return 0
	}
	return time.Duration(tokens / l.rate * float64(time.Second))
}

// take consumes one token when there is one.
func (l *apiRateLimiter) take(key string) rateDecision {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.bucket(key, l.now())
	if b.tokens < 1 {
		return l.decision(b, false)
	}
	b.tokens--
	return l.decision(b, true)
}

// peek reports whether one request would fit without consuming a token.
func (l *apiRateLimiter) peek(key string) rateDecision {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.entries[key]; !ok {
		return rateDecision{allowed: true, remaining: int(l.burst)}
	}
	b := l.bucket(key, l.now())
	return l.decision(b, b.tokens >= 1)
}

// drain consumes one token after the fact, down to an empty bucket.
func (l *apiRateLimiter) drain(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.bucket(key, l.now())
	b.tokens = math.Max(0, b.tokens-1)
}

// setHeaders writes the RateLimit fields of draft-ietf-httpapi-ratelimit-
// headers (the RateLimit-Limit/-Remaining/-Reset/-Policy spelling clients
// and proxies read today): the bucket size, the whole requests left, the
// seconds until the bucket is full and the policy as size;w=window.
func (l *apiRateLimiter) setHeaders(h http.Header, d rateDecision) {
	h.Set("RateLimit-Limit", strconv.Itoa(int(l.burst)))
	h.Set("RateLimit-Remaining", strconv.Itoa(d.remaining))
	h.Set("RateLimit-Reset", strconv.FormatInt(int64(math.Ceil(d.reset.Seconds())), 10))
	h.Set("RateLimit-Policy", l.policy)
}

// requestGuardsSpecification documents the guards of every operation in the
// OpenAPI document (x-jelee-request-guards): any operation that is not
// exempt may answer 429 rate_limited, 504 processing_timeout and 413
// body_too_large through its default error response.
func requestGuardsSpecification(cfg config.Config) map[string]any {
	limit := cfg.API.RateLimit
	rate := map[string]any{"enabled": limit.RateLimited()}
	if limit.RateLimited() {
		rate["requestsPerSecond"], rate["burst"] = limit.Rate(), limit.BurstSize()
	}
	return map[string]any{
		"order":     []string{"request-id", "recovery", "access-log", "trace", "timeout", "body-limit", "rate-limit (anonymous)", "authentication", "rate-limit (authenticated)", "authorization", "validation"},
		"rateLimit": rate, "timeoutSeconds": int64(cfg.API.Timeout(cfg.RequestTimeoutSeconds) / time.Second), "maxBodyBytes": cfg.API.BodyLimit(),
		"description": "Per instance, in memory. Authenticated requests count against their user and session token, others against the client address (trusted proxy rules). " +
			"Over the limit: 429 rate_limited with Retry-After; charged responses carry RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset and RateLimit-Policy. " +
			"Processing over the timeout: 504 processing_timeout. Direct delivery (/api/v1/sources/*), /images/*, playback information and the compatibility layer's streams, tracks, images and PlaybackInfo are exempt from all three guards; data and statistics exports from the timeout only.",
	}
}
