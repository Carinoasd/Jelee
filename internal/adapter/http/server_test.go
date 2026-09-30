package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

const (
	userID    = "11111111-1111-4111-8111-111111111111"
	sessionID = "22222222-2222-4222-8222-222222222222"
	itemID    = "33333333-3333-4333-8333-333333333333"
	sourceID  = "44444444-4444-4444-8444-444444444444"
	libraryID = "55555555-5555-4555-8555-555555555555"
)

type fakeBackend struct {
	readyError error
	authCalls  int
	auth       func(context.Context, string) (access.Principal, error)
}

func (b *fakeBackend) Ready(context.Context) error { return b.readyError }

func (b *fakeBackend) Authenticate(ctx context.Context, token string) (access.Principal, error) {
	b.authCalls++
	if b.auth != nil {
		return b.auth(ctx, token)
	}
	kind := access.ClientNative
	if token == strings.Repeat("w", 43) {
		kind = access.ClientWeb
	} else if token != strings.Repeat("n", 43) {
		return access.Principal{}, domain.ErrUnauthenticated
	}
	return access.Principal{UserID: userID, SessionID: sessionID, Kind: kind}, nil
}

type fakeRepository struct {
	listCalls int
	getCalls  int
	list      func(context.Context, string, string, int) ([]domain.Item, error)
	get       func(context.Context, string, string) (domain.Item, error)
}

func (r *fakeRepository) ListItems(ctx context.Context, user, cursor string, limit int) ([]domain.Item, error) {
	r.listCalls++
	if r.list != nil {
		return r.list(ctx, user, cursor, limit)
	}
	return []domain.Item{}, nil
}

func (r *fakeRepository) GetItem(ctx context.Context, user, id string) (domain.Item, error) {
	r.getCalls++
	if r.get != nil {
		return r.get(ctx, user, id)
	}
	return domain.Item{}, domain.ErrNotFound
}

type fakeResolver struct {
	calls   int
	resolve func(context.Context, access.Principal, string) (media.Source, error)
}

func (r *fakeResolver) Resolve(ctx context.Context, principal access.Principal, id string) (media.Source, error) {
	r.calls++
	if r.resolve != nil {
		return r.resolve(ctx, principal, id)
	}
	return media.Source{}, media.ErrNotFound
}

type fixture struct {
	backend    *fakeBackend
	repository *fakeRepository
	resolver   *fakeResolver
	logs       *bytes.Buffer
	handler    http.Handler
}

func validConfig() config.Config {
	return config.Config{
		Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost", "127.0.0.1", "::1"},
		DatabaseURL: "postgres://localhost/jelee", MaxConnections: 2, MaxStreams: 2,
		RequestTimeoutSeconds: 1,
	}
}

func newFixture(t *testing.T, catalog, direct bool) *fixture {
	t.Helper()
	f := &fixture{backend: &fakeBackend{}, repository: &fakeRepository{}, resolver: &fakeResolver{}, logs: &bytes.Buffer{}}
	cfg := validConfig()
	cfg.EnableCatalog, cfg.EnableDirect = catalog, direct
	handler, err := New(cfg, f.backend, app.NewCatalog(f.repository), f.resolver, slog.New(slog.NewJSONHandler(f.logs, nil)))
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	f.handler = handler
	return f
}

func TestConstructorRejectsInvalidConfigurationAndMissingDependencies(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*config.Config)
	}{
		{"direct-without-catalog", func(c *config.Config) { c.EnableDirect = true }},
		{"missing-database", func(c *config.Config) { c.DatabaseURL = "" }},
		{"sqlite-database", func(c *config.Config) { c.DatabaseURL = "sqlite:///data.db" }},
		{"unbounded-streams", func(c *config.Config) { c.MaxStreams = 0 }},
		{"unbounded-timeout", func(c *config.Config) { c.RequestTimeoutSeconds = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.change(&cfg)
			handler, err := New(cfg, &fakeBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.Default())
			if err == nil || handler != nil {
				t.Fatal("invalid configuration produced a router")
			}
		})
	}
	for _, missing := range []string{"backend", "catalog", "resolver", "logger"} {
		t.Run(missing, func(t *testing.T) {
			var backend Backend = &fakeBackend{}
			catalog := app.NewCatalog(&fakeRepository{})
			var resolver media.Resolver = &fakeResolver{}
			logger := slog.Default()
			switch missing {
			case "backend":
				backend = nil
			case "catalog":
				catalog = nil
			case "resolver":
				resolver = nil
			case "logger":
				logger = nil
			}
			if handler, err := New(validConfig(), backend, catalog, resolver, logger); err == nil || handler != nil {
				t.Fatal("missing dependency produced a router")
			}
		})
	}
}

func (f *fixture) request(method, target, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+target, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func assertProblem(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, status, w.Body.String())
	}
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
			TraceID string         `json:"traceId"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error response is not JSON: %v", err)
	}
	if body.Error.Code != code || body.Error.Message == "" || body.Error.Details == nil {
		t.Fatalf("invalid error contract: %+v", body.Error)
	}
	if id := w.Header().Get("X-Request-ID"); id == "" || body.Error.TraceID != id {
		t.Fatalf("response traceId does not match request ID: %+v", body.Error)
	}
}

func TestPublicHealthAndHostBoundary(t *testing.T) {
	f := newFixture(t, false, false)
	for _, host := range []string{"localhost", "LOCALHOST:8097", "127.0.0.1:8097", "[::1]:8097"} {
		t.Run(host, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
			r.Host = host
			r.Header.Set("X-Forwarded-Host", "attacker.invalid")
			r.Header.Set("Forwarded", "host=attacker.invalid;for=192.168.7.7")
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			if w.Code != 200 || len(w.Header().Get("X-Request-ID")) != 32 {
				t.Fatalf("health response: status %d headers %v", w.Code, w.Header())
			}
			for name, value := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store", "X-Jelee-Dev-Mode": "false"} {
				if got := w.Header().Get(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
			if strings.Contains(w.Body.String(), "attacker.invalid") || w.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("public response reflected attacker host or omitted CSP")
			}
		})
	}
	for _, host := range []string{"attacker.invalid", "localhost.attacker.invalid", "localhost@attacker.invalid", "localhost:notaport", "localhost:0", "localhost:65536", "[::1", "::1]", "localhost[", "[localhost]:8097", "localhost:+80", "[::1]:+80"} {
		t.Run("reject/"+host, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
			r.Host = host
			r.Header.Set("X-Forwarded-Host", "localhost")
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			assertProblem(t, w, 400, "invalid_host")
		})
	}
	if f.backend.authCalls != 0 {
		t.Fatal("health/invalid Host invoked authentication")
	}
}

func TestRolloutFlagsAndCapabilities(t *testing.T) {
	for _, flags := range []struct{ catalog, direct bool }{{false, false}, {true, false}, {true, true}} {
		t.Run(fmt.Sprintf("catalog=%t/direct=%t", flags.catalog, flags.direct), func(t *testing.T) {
			f := newFixture(t, flags.catalog, flags.direct)
			w := f.request(http.MethodGet, "/api/v1/system", "")
			var system struct {
				Data struct {
					Name         string          `json:"name"`
					DevMode      bool            `json:"devMode"`
					Capabilities map[string]bool `json:"capabilities"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &system); err != nil || w.Code != 200 {
				t.Fatalf("system response: %d %s", w.Code, w.Body.String())
			}
			if system.Data.Name != "Jelee" || system.Data.DevMode || system.Data.Capabilities["catalog"] != flags.catalog || system.Data.Capabilities["directDelivery"] != flags.direct {
				t.Fatalf("capabilities mismatch: %+v", system)
			}
			for _, capability := range []string{"transcoding", "hls", "dash", "remux", "downloads"} {
				value, exists := system.Data.Capabilities[capability]
				if !exists || value {
					t.Errorf("forbidden capability %q absent or enabled", capability)
				}
			}
			if !flags.catalog {
				assertProblem(t, f.request(http.MethodGet, "/api/v1/items", strings.Repeat("n", 43)), 404, "not_found")
			}
			if !flags.direct {
				assertProblem(t, f.request(http.MethodGet, "/api/v1/sources/"+sourceID+"/stream", strings.Repeat("n", 43)), 404, "not_found")
			}
			assertProblem(t, f.request(http.MethodGet, "/debug/pprof/heap", ""), 404, "not_found")
			if f.resolver.calls != 0 {
				t.Fatal("disabled route reached media resolver")
			}
		})
	}
}

func TestAuthenticationAndStrictCatalogQueries(t *testing.T) {
	f := newFixture(t, true, false)
	for _, token := range []string{"", "short", strings.Repeat("x", 43)} {
		assertProblem(t, f.request(http.MethodGet, "/api/v1/items", token), 401, "authentication_required")
	}
	if f.repository.listCalls != 0 {
		t.Fatal("unauthenticated request reached catalog")
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=-1", "limit=abc", "limit=1&limit=2", "cursor=bad", "cursor=" + strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), "sort=title", "filter%ZZ=one", "limit=1;cursor=x"} {
		t.Run(query, func(t *testing.T) {
			assertProblem(t, f.request(http.MethodGet, "/api/v1/items?"+query, strings.Repeat("n", 43)), 400, "invalid_request")
		})
	}
	if f.repository.listCalls != 0 {
		t.Fatal("invalid query reached repository")
	}
	f.repository.list = func(ctx context.Context, user, cursor string, limit int) ([]domain.Item, error) {
		if user != userID || cursor != itemID || limit != 2 {
			t.Errorf("catalog arguments = %q %q %d", user, cursor, limit)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("catalog lookup lacks request deadline")
		}
		return []domain.Item{{ID: itemID, LibraryID: libraryID, Kind: "Movie", Title: "First"}, {ID: sourceID, LibraryID: libraryID, Kind: "Episode", Title: "Second"}}, nil
	}
	w := f.request(http.MethodGet, "/api/v1/items?limit=2&cursor="+itemID, strings.Repeat("n", 43))
	var page struct {
		Data       []domain.Item `json:"data"`
		Pagination struct {
			NextCursor string `json:"nextCursor"`
			Limit      int    `json:"limit"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Data) != 2 || page.Pagination.NextCursor != sourceID || page.Pagination.Limit != 2 {
		t.Fatalf("pagination contract: %d %s", w.Code, w.Body.String())
	}
}

func TestErrorLanguageNegotiationAtHTTPBoundary(t *testing.T) {
	f := newFixture(t, true, false)
	for _, tc := range []struct{ header, locale, message string }{
		{"", "zh-CN", "请先登录。"},
		{"zh-TW", "zh-TW", "請先登入。"},
		{"ja-JP", "ja-JP", "ログインしてください。"},
		{"en-US", "en-US", "Authentication is required."},
		{"fr-FR", "en-US", "Authentication is required."},
	} {
		t.Run(tc.header, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/items", nil)
			r.Header.Set("Accept-Language", tc.header)
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			assertProblem(t, w, 401, "authentication_required")
			if w.Header().Get("Content-Language") != tc.locale || !strings.Contains(w.Header().Get("Vary"), "Accept-Language") || !strings.Contains(w.Body.String(), tc.message) {
				t.Fatalf("localized error contract: headers %v body %s", w.Header(), w.Body.String())
			}
		})
	}
}

func TestItemVisibilityAndOpaqueFailures(t *testing.T) {
	f := newFixture(t, true, false)
	assertProblem(t, f.request(http.MethodGet, "/api/v1/items/not-a-uuid", strings.Repeat("n", 43)), 404, "not_found")
	if f.repository.getCalls != 0 {
		t.Fatal("invalid item ID reached database")
	}
	missing := f.request(http.MethodGet, "/api/v1/items/"+itemID, strings.Repeat("n", 43))
	assertProblem(t, missing, 404, "not_found")
	f.repository.get = func(context.Context, string, string) (domain.Item, error) {
		return domain.Item{}, fmt.Errorf("hidden item lookup: %w", domain.ErrNotFound)
	}
	hidden := f.request(http.MethodGet, "/api/v1/items/"+itemID, strings.Repeat("n", 43))
	assertProblem(t, hidden, 404, "not_found")
	for _, reason := range []string{"postgres://admin:super-secret@10.0.0.7/jelee", `C:\private\media\hidden.mkv`, "SELECT secret FROM internal_users"} {
		f.repository.get = func(context.Context, string, string) (domain.Item, error) { return domain.Item{}, errors.New(reason) }
		w := f.request(http.MethodGet, "/api/v1/items/"+itemID, strings.Repeat("n", 43))
		assertProblem(t, w, 500, "internal_error")
		if strings.Contains(w.Body.String(), reason) || strings.Contains(f.logs.String(), reason) {
			t.Fatal("underlying repository error leaked")
		}
	}
	f.repository.get = func(context.Context, string, string) (domain.Item, error) { panic("private-path-and-token") }
	assertProblem(t, f.request(http.MethodGet, "/api/v1/items/"+itemID, strings.Repeat("n", 43)), 500, "internal_error")
	if strings.Contains(f.logs.String(), "private-path-and-token") || strings.Contains(f.logs.String(), strings.Repeat("n", 43)) {
		t.Fatal("panic value or bearer token leaked into logs")
	}
}

func TestReadinessDoesNotLeakDependencyErrors(t *testing.T) {
	f := newFixture(t, false, false)
	f.backend.readyError = errors.New("postgres://root:secret@192.168.0.15/private")
	w := f.request(http.MethodGet, "/readyz", "")
	assertProblem(t, w, 503, "not_ready")
	if strings.Contains(w.Body.String(), "secret") || strings.Contains(f.logs.String(), "192.168") {
		t.Fatal("readiness leaked credentials or internal address")
	}
	if got := f.request(http.MethodGet, "/healthz", "").Code; got != 200 {
		t.Fatalf("dependency failure changed liveness: %d", got)
	}
}

func TestTransformationRoutesAndParametersAreRejected(t *testing.T) {
	f := newFixture(t, true, true)
	for _, path := range []string{"/Videos/" + sourceID + "/hls/master.m3u8", "/DASH/manifest.mpd", "/transcode", "/api/v1/segments/1", "/%68ls/master", "/%2568ls/master"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			assertProblem(t, f.request(method, path, ""), 409, "transcode_disabled")
		}
	}
	for _, query := range []string{"VideoCodec=h264", "maxVideoBitrate=100000", "audioCodec=aac", "TranscodeReasons=ContainerNotSupported", "SegmentLength=5", "protocol=hls", "container=dash", "subtitleMethod=burnin"} {
		assertProblem(t, f.request(http.MethodGet, "/api/v1/sources/"+sourceID+"/stream?"+query, strings.Repeat("n", 43)), 409, "transcode_disabled")
	}
	if f.resolver.calls != 0 {
		t.Fatal("transformation request reached source resolver")
	}
}

func TestWebCannotSpoofNativePlaybackAndNativeRangePreservesBytes(t *testing.T) {
	f := newFixture(t, true, true)
	root := t.TempDir()
	original := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	path := filepath.Join(root, "sample.mp4")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	f.resolver.resolve = func(ctx context.Context, p access.Principal, id string) (media.Source, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
			t.Error("media source lookup does not honor configured request timeout")
		}
		if p.UserID != userID || p.SessionID != sessionID || p.Kind != access.ClientNative || id != sourceID {
			t.Errorf("untrusted source identity: %+v %q", p, id)
		}
		return media.Source{Root: root, RelativePath: "sample.mp4", ContentType: "video/mp4"}, nil
	}
	r := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/sources/"+sourceID+"/stream", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("w", 43))
	r.Header.Set("User-Agent", "Native TV Client")
	r.Header.Set("X-Client-Kind", "native")
	r.Header.Set("X-Jelee-Client", "native")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	assertProblem(t, w, 403, "web_playback_disabled")
	if f.resolver.calls != 0 {
		t.Fatal("Web session reached source resolver after header spoofing")
	}
	r = httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/sources/"+sourceID+"/stream", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("n", 43))
	r.Header.Set("Range", "bytes=4-9")
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "456789" || w.Header().Get("Content-Range") != "bytes 4-9/36" {
		t.Fatalf("native range response: %d %s %v", w.Code, w.Body.String(), w.Header())
	}
	if w.Header().Get("Content-Disposition") != "" {
		t.Fatal("direct media response exposed explicit download disposition")
	}
	head := f.request(http.MethodHead, "/api/v1/sources/"+sourceID+"/stream", strings.Repeat("n", 43))
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "36" {
		t.Fatalf("HEAD response contract: %d %v", head.Code, head.Header())
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(after) != sha256.Sum256(original) {
		t.Fatal("direct delivery changed original resource")
	}
}

func TestCentralErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrNotFound, 404, "not_found"}, {media.ErrNotFound, 404, "not_found"},
		{domain.ErrUnauthenticated, 401, "authentication_required"}, {media.ErrUnauthenticated, 401, "authentication_required"},
		{domain.ErrInvalid, 400, "invalid_request"}, {media.ErrInvalidRequest, 400, "invalid_request"},
		{media.ErrPlaybackDenied, 403, "web_playback_disabled"}, {media.ErrTranscodeDisabled, 409, "transcode_disabled"},
		{media.ErrBusy, 429, "stream_limit"}, {media.ErrMethodNotAllowed, 405, "method_not_allowed"},
		{media.ErrLookupTimeout, 504, "lookup_timeout"},
		{media.ErrInvalidRange, 416, "invalid_range"}, {media.ErrPreconditionFailed, 412, "precondition_failed"},
		{media.ErrBodyTooLarge, 413, "body_too_large"}, {media.ErrUnsupportedMediaType, 415, "unsupported_media_type"},
		{errors.New("sensitive internal details"), 500, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.err.Error(), func(t *testing.T) {
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "contract-trace")
			WriteError(w, httptest.NewRequest(http.MethodGet, "/", nil), fmt.Errorf("private diagnostic context: %w", tc.err))
			assertProblem(t, w, tc.status, tc.code)
			if strings.Contains(w.Body.String(), "private diagnostic") || strings.Contains(w.Body.String(), "sensitive internal") {
				t.Fatal("wrapped error details leaked")
			}
		})
	}
}
