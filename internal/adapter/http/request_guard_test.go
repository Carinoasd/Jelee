package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type guardClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *guardClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *guardClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type guardFixture struct {
	*fixture
	clock *guardClock
	stats *RequestGuardStats
	logs  *syncBuffer
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newGuardFixture serves the catalog and direct delivery with the general
// rate limit on: a bucket of burst requests refilled at one per second.
func newGuardFixture(t *testing.T, burst int, change func(*config.Config), options ...Option) *guardFixture {
	t.Helper()
	f := &guardFixture{fixture: &fixture{backend: &fakeBackend{}, repository: &fakeRepository{}, resolver: &fakeResolver{}},
		clock: &guardClock{now: time.Unix(1_800_000_000, 0)}, stats: NewRequestGuardStats(), logs: &syncBuffer{}}
	cfg := validConfig()
	cfg.EnableCatalog, cfg.EnableDirect = true, true
	cfg.API = config.APIConfig{RateLimit: config.APIRateLimitConfig{RequestsPerSecond: 1, Burst: burst}}
	if change != nil {
		change(&cfg)
	}
	options = append([]Option{WithRequestGuardStats(f.stats), func(s *Server) { s.guardNow = f.clock.Now }}, options...)
	handler, err := NewWithImages(cfg, f.backend, app.NewCatalog(f.repository), f.resolver, slog.New(slog.NewJSONHandler(f.logs, nil)), nil, nil, nil, nil, nil, options...)
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handler
	return f
}

func (f *guardFixture) send(method, target, token, remote string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+target, nil)
	if remote != "" {
		r.RemoteAddr = remote
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func assertRateHeaders(t *testing.T, w *httptest.ResponseRecorder, limit, remaining int) {
	t.Helper()
	h := w.Header()
	if h.Get("RateLimit-Limit") != strconv.Itoa(limit) || h.Get("RateLimit-Remaining") != strconv.Itoa(remaining) || h.Get("RateLimit-Reset") == "" ||
		h.Get("RateLimit-Policy") != strconv.Itoa(limit)+";w="+strconv.Itoa(limit) {
		t.Fatalf("rate limit fields: %v", h)
	}
}

func TestRateLimitAnonymousRequestsPerClientAddress(t *testing.T) {
	f := newGuardFixture(t, 3, func(c *config.Config) { c.TrustedProxies = []string{"198.51.100.0/24"} })
	for i := range 3 {
		w := f.send("GET", "/api/v1/system", "", "192.0.2.1:1000")
		if w.Code != 200 {
			t.Fatalf("request %d: %d", i, w.Code)
		}
		assertRateHeaders(t, w, 3, 2-i)
	}
	w := f.send("GET", "/api/v1/system", "", "192.0.2.1:1001")
	assertProblem(t, w, 429, "rate_limited")
	if w.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q", w.Header().Get("Retry-After"))
	}
	assertRateHeaders(t, w, 3, 0)
	// A forwarding header from an untrusted peer does not open a new bucket.
	assertProblem(t, f.send("GET", "/api/v1/system", "", "192.0.2.1:1002", "X-Forwarded-For", "203.0.113.9"), 429, "rate_limited")
	// Another address has its own bucket, and so does a client behind a
	// trusted proxy, decided by the shared proxy rules.
	if w := f.send("GET", "/api/v1/system", "", "192.0.2.2:1000"); w.Code != 200 {
		t.Fatalf("other address: %d", w.Code)
	}
	if w := f.send("GET", "/api/v1/system", "", "198.51.100.7:1000", "X-Forwarded-For", "203.0.113.9"); w.Code != 200 {
		t.Fatalf("client behind a trusted proxy: %d", w.Code)
	}
	f.clock.Advance(time.Second)
	if w := f.send("GET", "/api/v1/system", "", "192.0.2.1:1003"); w.Code != 200 {
		t.Fatalf("after refill: %d", w.Code)
	}
	if f.stats.RateLimitedTotal() != 2 {
		t.Fatalf("rate limited total = %d", f.stats.RateLimitedTotal())
	}
	// Health probes and the frontend are outside the API limit.
	for range 5 {
		if w := f.send("GET", "/healthz", "", "192.0.2.1:1004"); w.Code != 200 || w.Header().Get("RateLimit-Limit") != "" {
			t.Fatalf("health probe limited: %d %v", w.Code, w.Header())
		}
	}
}

func TestRateLimitAuthenticatedRequestsPerUserAndToken(t *testing.T) {
	f := newGuardFixture(t, 3, nil)
	f.repository.get = func(context.Context, string, string) (domain.Item, error) {
		return domain.Item{ID: itemID}, nil
	}
	for i := range 3 {
		w := f.send("GET", "/api/v1/items/"+itemID, nativeToken, "192.0.2.1:1000")
		if w.Code != 200 {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
		assertRateHeaders(t, w, 3, 2-i)
	}
	calls := f.repository.getCalls
	w := f.send("GET", "/api/v1/items/"+itemID, nativeToken, "192.0.2.1:1000")
	assertProblem(t, w, 429, "rate_limited")
	if w.Header().Get("Retry-After") != "1" || f.repository.getCalls != calls {
		t.Fatalf("over the limit: Retry-After %q, route ran %d", w.Header().Get("Retry-After"), f.repository.getCalls-calls)
	}
	// The same user from another address shares the token's bucket; the
	// address itself was never charged.
	assertProblem(t, f.send("GET", "/api/v1/items/"+itemID, nativeToken, "192.0.2.9:1000"), 429, "rate_limited")
	for range 3 {
		if w := f.send("GET", "/api/v1/system", "", "192.0.2.1:1000"); w.Code != 200 {
			t.Fatalf("address bucket charged by authenticated requests: %d", w.Code)
		}
	}
}

func TestRateLimitChargesFailedAuthenticationToTheAddress(t *testing.T) {
	f := newGuardFixture(t, 3, nil)
	bad := strings.Repeat("x", 43)
	for range 3 {
		assertProblem(t, f.send("GET", "/api/v1/items/"+itemID, bad, "192.0.2.1:1000"), 401, "authentication_required")
	}
	calls := f.backend.authCalls
	assertProblem(t, f.send("GET", "/api/v1/items/"+itemID, bad, "192.0.2.1:1000"), 429, "rate_limited")
	if f.backend.authCalls != calls {
		t.Fatal("an exhausted address still reached the credential lookup")
	}
	// Valid credentials from the same address are refused too until the
	// address bucket refills; other addresses are unaffected.
	assertProblem(t, f.send("GET", "/api/v1/items/"+itemID, nativeToken, "192.0.2.1:1000"), 429, "rate_limited")
	if w := f.send("GET", "/api/v1/items/"+itemID, nativeToken, "192.0.2.2:1000"); w.Code != 404 {
		t.Fatalf("other address: %d", w.Code)
	}
}

func serveSample(t *testing.T, f *guardFixture) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.mp4"), []byte("0123456789abcdefghijklmnopqrstuvwxyz"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.resolver.resolve = func(context.Context, access.Principal, string) (media.Source, error) {
		return media.Source{Root: root, RelativePath: "sample.mp4", ContentType: "video/mp4"}, nil
	}
}

// Playback must never be interrupted by the general limit: Range requests
// of a session whose API bucket is empty still stream.
func TestRateLimitNeverAppliesToDirectDelivery(t *testing.T) {
	f := newGuardFixture(t, 2, nil)
	serveSample(t, f)
	for range 2 {
		f.send("GET", "/api/v1/items/"+itemID, nativeToken, "192.0.2.1:1000")
	}
	assertProblem(t, f.send("GET", "/api/v1/items/"+itemID, nativeToken, "192.0.2.1:1000"), 429, "rate_limited")
	for i := range 50 {
		w := f.send("GET", "/api/v1/sources/"+sourceID+"/stream", nativeToken, "192.0.2.1:1000", "Range", "bytes="+strconv.Itoa(i%30)+"-35")
		if w.Code != 206 || w.Header().Get("RateLimit-Limit") != "" {
			t.Fatalf("range request %d: %d %v", i, w.Code, w.Header())
		}
	}
	if w := f.send("GET", "/api/v1/items/"+itemID+"/playback", nativeToken, "192.0.2.1:1000"); w.Code == 429 {
		t.Fatal("playback information was rate limited")
	}
}

func TestRateLimitOffAppliesNoLimitAndNoFields(t *testing.T) {
	f := newGuardFixture(t, 1, func(c *config.Config) { c.API.RateLimit.Mode = "off" })
	for i := range 10 {
		w := f.send("GET", "/api/v1/system", "", "192.0.2.1:1000")
		if w.Code != 200 || w.Header().Get("RateLimit-Limit") != "" {
			t.Fatalf("request %d: %d %v", i, w.Code, w.Header())
		}
	}
}

func TestRequestTimeoutCancelsSlowWorkAndSpareStreams(t *testing.T) {
	f := newGuardFixture(t, 100, nil, func(s *Server) { s.guardTimeout = 50 * time.Millisecond })
	var cause error
	f.repository.get = func(ctx context.Context, _, _ string) (domain.Item, error) {
		<-ctx.Done()
		cause = context.Cause(ctx)
		return domain.Item{}, ctx.Err()
	}
	start := time.Now()
	w := f.send("GET", "/api/v1/items/"+itemID, nativeToken, "")
	assertProblem(t, w, 504, "processing_timeout")
	if !errors.Is(cause, errProcessingTimeout) || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("storage saw cause %v after %v", cause, time.Since(start))
	}
	if f.stats.TimeoutsTotal() != 1 || !strings.Contains(f.logs.String(), "request processing timed out") {
		t.Fatalf("timeout not counted or logged: %d %s", f.stats.TimeoutsTotal(), f.logs.String())
	}
	// A media lookup slower than the processing timeout still streams.
	serveSample(t, f)
	sample := f.resolver.resolve
	f.resolver.resolve = func(ctx context.Context, p access.Principal, id string) (media.Source, error) {
		time.Sleep(150 * time.Millisecond)
		if errors.Is(context.Cause(ctx), errProcessingTimeout) {
			t.Error("the stream lookup carries the processing timeout")
		}
		return sample(ctx, p, id)
	}
	if w := f.send("GET", "/api/v1/sources/"+sourceID+"/stream", nativeToken, "", "Range", "bytes=0-3"); w.Code != 206 || w.Body.String() != "0123" {
		t.Fatalf("stream: %d %s", w.Code, w.Body)
	}
	if f.stats.TimeoutsTotal() != 1 {
		t.Fatal("the stream was counted as timed out")
	}
}

func TestTimeoutWriterKeepsResponsesSentInTime(t *testing.T) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), time.Hour, errProcessingTimeout)
	defer cancel()
	r := httptest.NewRequest("GET", "http://localhost/api/v1/system", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	tw := &timeoutWriter{ResponseWriter: rec, r: r}
	if _, err := io.WriteString(tw, "ok"); err != nil || rec.Code != 200 || rec.Body.String() != "ok" || tw.timedOut || tw.Unwrap() != rec {
		t.Fatalf("response in time: %d %q %v", rec.Code, rec.Body, err)
	}
	cancel()
	expiredCtx, stop := context.WithTimeoutCause(context.Background(), 0, errProcessingTimeout)
	defer stop()
	rec = httptest.NewRecorder()
	rec.Header().Set("Content-Length", "2")
	tw = &timeoutWriter{ResponseWriter: rec, r: httptest.NewRequest("GET", "http://localhost/compat/System/Info", nil).WithContext(expiredCtx)}
	if _, err := io.WriteString(tw, "ok"); !errors.Is(err, http.ErrHandlerTimeout) || rec.Code != 504 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "0" {
		t.Fatalf("expired compatibility response: %d %q %v %v", rec.Code, rec.Body, err, rec.Header())
	}
	tw.WriteHeader(200)
	if rec.Code != 504 {
		t.Fatal("a later status replaced the timeout")
	}
}

// G08.1: request ID, recovery and log, trace, timeout, then authentication
// and the authenticated rate limit, then the route's own work.
func TestRequestGuardOrder(t *testing.T) {
	f := newGuardFixture(t, 5, nil)
	var authSaw, routeSaw context.Context
	f.backend.auth = func(ctx context.Context, _ string) (access.Principal, error) {
		authSaw = ctx
		return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative}, nil
	}
	var headersAtRoute http.Header
	f.repository.get = func(ctx context.Context, _, _ string) (domain.Item, error) {
		routeSaw = ctx
		return domain.Item{ID: itemID}, nil
	}
	h := f.handler
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		headersAtRoute = w.Header().Clone()
	})
	w := f.send("GET", "/api/v1/items/"+itemID, nativeToken, "")
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	for name, ctx := range map[string]context.Context{"authentication": authSaw, "route": routeSaw} {
		span, traced := domain.SpanFromContext(ctx)
		if _, deadline := ctx.Deadline(); !traced || !deadline || span.TraceHex() != w.Header().Get("X-Request-ID") {
			t.Fatalf("%s runs outside the trace or the timeout", name)
		}
	}
	if _, ok := access.PrincipalFromContext(routeSaw); !ok {
		t.Fatal("route ran without the principal")
	}
	parent := headersAtRoute.Get("traceparent")
	if !strings.HasPrefix(parent, "00-"+w.Header().Get("X-Request-ID")+"-") || len(parent) != 55 {
		t.Fatalf("traceparent %q", parent)
	}
	// Charged after authentication: the bucket is the token's, not the
	// address's (which stays full).
	assertRateHeaders(t, w, 5, 4)
	if w := f.send("GET", "/api/v1/system", "", ""); w.Header().Get("RateLimit-Remaining") != "4" {
		t.Fatalf("address bucket: %v", w.Header())
	}
	// An inbound traceparent is not adopted.
	w = f.send("GET", "/api/v1/system", "", "", "traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if strings.Contains(w.Header().Get("traceparent"), "4bf92f3577b34da6a3ce929d0e0e4736") {
		t.Fatal("client chose the trace")
	}
}

func TestRequestBodyCeiling(t *testing.T) {
	f := newGuardFixture(t, 100, func(c *config.Config) { c.API.MaxBodyBytes = 2 << 20 })
	r := httptest.NewRequest("POST", "http://localhost/api/v1/items/"+itemID+"/sources/check", strings.NewReader("{}"))
	r.ContentLength = 3 << 20
	r.Header.Set("Authorization", "Bearer "+nativeToken)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	assertProblem(t, w, 413, "body_too_large")
	if f.backend.authCalls != 0 {
		t.Fatal("an oversized body reached authentication")
	}
	r = httptest.NewRequest("POST", "http://localhost/compat/Sessions/Playing", strings.NewReader("{}"))
	r.ContentLength = 3 << 20
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 413 || w.Body.Len() != 0 {
		t.Fatalf("compatibility body ceiling: %d %q", w.Code, w.Body)
	}
}

func TestGuardPolicyClassifiesRoutes(t *testing.T) {
	cfg := validConfig()
	cfg.Images.MaxSourceBytes = 32 << 20
	s := &Server{cfg: cfg}
	body := cfg.API.BodyLimit()
	for _, tc := range []struct {
		method, path string
		want         guardPolicy
	}{
		{"GET", "/api/v1/sources/" + sourceID + "/stream", guardPolicy{}},
		{"HEAD", "/api/v1/sources/" + sourceID + "/subtitles/" + itemID, guardPolicy{}},
		{"GET", "/api/v1/sources/" + sourceID + "/embedded-subtitles/2.sub", guardPolicy{}},
		{"GET", "/images/primary/" + itemID, guardPolicy{}},
		{"GET", "/api/v1/items/" + itemID + "/playback", guardPolicy{}},
		{"POST", "/api/v1/items/" + itemID + "/playback/check", guardPolicy{}},
		{"GET", "/compat/Videos/" + itemID + "/stream.mkv", guardPolicy{}},
		{"GET", "/COMPAT/videos/" + itemID + "/" + sourceID + "/Subtitles/3/0/Stream.srt", guardPolicy{}},
		{"POST", "/compat/Items/" + itemID + "/PlaybackInfo", guardPolicy{}},
		{"GET", "/compat/Items/" + itemID + "/Images/Primary/0", guardPolicy{}},
		{"GET", "/compat/Audio/" + itemID + "/stream", guardPolicy{}},
		{"GET", "/api/v1/items/" + itemID, guardPolicy{rate: true, timeout: true, body: body}},
		{"GET", "/api/v1/items/" + itemID + "/playback/history", guardPolicy{rate: true, timeout: true, body: body}},
		{"POST", "/compat/Sessions/Playing/Progress", guardPolicy{rate: true, timeout: true, body: body}},
		{"GET", "/compat/Items", guardPolicy{rate: true, timeout: true, body: body}},
		{"GET", "/api/v1/users/" + userID + "/data-export", guardPolicy{rate: true, body: body}},
		{"GET", "/api/v1/watch-stats/export", guardPolicy{rate: true, body: body}},
		{"GET", "/debug/pprof/profile", guardPolicy{body: body}},
		{"POST", "/api/v1/items/" + itemID + "/images/primary", guardPolicy{rate: true, timeout: true, body: 32 << 20}},
		{"GET", "/healthz", guardPolicy{timeout: true, body: body}},
		{"GET", "/assets/index.js", guardPolicy{timeout: true, body: body}},
	} {
		r := httptest.NewRequest(tc.method, "http://localhost"+tc.path, nil)
		if got := s.guardPolicy(r); got != tc.want {
			t.Errorf("%s %s: %+v, want %+v", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestAPIRateLimiterBucketsRefillAndEvict(t *testing.T) {
	clock := &guardClock{now: time.Unix(0, 0)}
	l := newAPIRateLimiter(2, 2, 2, clock.Now)
	if d := l.peek("a"); !d.allowed || d.remaining != 2 || len(l.entries) != 0 {
		t.Fatalf("peek of a new key: %+v", d)
	}
	l.take("a")
	l.take("a")
	d := l.take("a")
	if d.allowed || d.retry != 500*time.Millisecond || d.reset != time.Second {
		t.Fatalf("empty bucket: %+v", d)
	}
	l.drain("a")
	if l.entries["a"].Value.(*rateBucket).tokens != 0 {
		t.Fatal("drain went below empty")
	}
	clock.Advance(500 * time.Millisecond)
	if d := l.peek("a"); !d.allowed || d.remaining != 1 {
		t.Fatalf("after refill: %+v", d)
	}
	clock.Advance(time.Hour)
	if d := l.take("a"); d.remaining != 1 {
		t.Fatalf("refill above the burst: %+v", d)
	}
	l.take("b")
	l.take("c")
	if _, ok := l.entries["a"]; ok || len(l.entries) != 2 || l.order.Len() != 2 {
		t.Fatal("least recently used key not evicted")
	}
	if l.policy != "2;w=1" {
		t.Fatalf("policy %q", l.policy)
	}
}

func TestRequestGuardStatsNilReadsZero(t *testing.T) {
	var g *RequestGuardStats
	if g.RateLimitedTotal() != 0 || g.TimeoutsTotal() != 0 {
		t.Fatal("nil stats")
	}
	if retrySeconds(0) != 1 || retrySeconds(1500*time.Millisecond) != 2 {
		t.Fatal("retry rounding")
	}
}

func TestRateLimitCompatibilityLayerAnswersLikeTheLayer(t *testing.T) {
	f := newGuardFixture(t, 2, func(c *config.Config) { c.EnableCompat = true })
	f.backend.auth = func(_ context.Context, token string) (access.Principal, error) {
		if token != compatNativeToken {
			return access.Principal{}, domain.ErrUnauthenticated
		}
		return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative}, nil
	}
	for range 2 {
		if w := f.send("GET", "/compat/System/Info?ApiKey="+compatNativeToken, "", "192.0.2.1:1000"); w.Code != 200 {
			t.Fatalf("system info: %d %s", w.Code, w.Body)
		}
	}
	w := f.send("GET", "/compat/System/Info?ApiKey="+compatNativeToken, "", "192.0.2.1:1000")
	if w.Code != 429 || w.Body.Len() != 0 || w.Header().Get("Retry-After") != "1" || w.Header().Get("RateLimit-Remaining") != "0" {
		t.Fatalf("authenticated over the limit: %d %q %v", w.Code, w.Body, w.Header())
	}
	// Anonymous requests count against the address, which is still full.
	for range 2 {
		if w := f.send("GET", "/compat/System/Info/Public", "", "192.0.2.1:1000"); w.Code != 200 {
			t.Fatalf("public info: %d", w.Code)
		}
	}
	w = f.send("GET", "/compat/system/info/public", "", "192.0.2.1:1000")
	if w.Code != 429 || w.Body.Len() != 0 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("anonymous over the limit: %d %q %v", w.Code, w.Body, w.Header())
	}
	if f.stats.RateLimitedTotal() != 2 {
		t.Fatalf("rate limited total = %d", f.stats.RateLimitedTotal())
	}
}
