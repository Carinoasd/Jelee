package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/compat"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var (
	compatNativeToken = strings.Repeat("A", 43)
	compatWebToken    = strings.Repeat("Q", 43)
)

func compatFixture(t *testing.T, enabled bool) (*fixture, func(method, target string, header http.Header) *httptest.ResponseRecorder) {
	t.Helper()
	f := &fixture{backend: &fakeBackend{auth: func(_ context.Context, token string) (access.Principal, error) {
		switch token {
		case compatNativeToken:
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative}, nil
		case compatWebToken:
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientWeb}, nil
		}
		return access.Principal{}, domain.ErrUnauthenticated
	}}, repository: &fakeRepository{}, resolver: &fakeResolver{}}
	cfg := validConfig()
	cfg.EnableCatalog, cfg.EnableDirect, cfg.EnableCompat = true, true, enabled
	handler, err := New(cfg, f.backend, app.NewCatalog(f.repository), f.resolver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handler
	return f, func(method, target string, header http.Header) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, compatRequest(method, target, header))
		return w
	}
}

// compatAuth carries the token in the legacy query parameter; the header
// forms are covered by the compat package's own tests.
func compatAuth(token string) http.Header {
	return http.Header{compatTokenHeader: {token}}
}

// compatTokenHeader is a test-only marker moved into the ApiKey query
// parameter by the request helpers.
const compatTokenHeader = "X-Test-Compat-Token"

func compatRequest(method, target string, header http.Header) *http.Request {
	r := httptest.NewRequest(method, "http://localhost"+target, nil)
	for k, vs := range header {
		if k == compatTokenHeader {
			q := r.URL.Query()
			q.Set("ApiKey", vs[0])
			r.URL.RawQuery = q.Encode()
			continue
		}
		r.Header[k] = vs
	}
	return r
}

func TestCompatDisabledByDefaultReservesPrefix(t *testing.T) {
	f, do := compatFixture(t, false)
	for _, path := range []string{"/compat", "/compat/System/Info/Public", "/COMPAT/System/Ping"} {
		assertProblem(t, do(http.MethodGet, path, nil), 404, "not_found")
	}
	// Removed families stay refused below the prefix even when the layer is off.
	assertProblem(t, do(http.MethodGet, "/compat/LiveTv/Info", nil), 501, "feature_removed")
	if f.backend.authCalls != 0 {
		t.Fatal("disabled layer reached the session store")
	}
}

func TestCompatMountedBehindServerBoundary(t *testing.T) {
	f, do := compatFixture(t, true)
	for _, path := range []string{"/compat/System/Info/Public", "/COMPAT/system/info/public", "/Compat/System/Info/Public/"} {
		w := do(http.MethodGet, path, nil)
		if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"ServerName":"Jelee"`) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		// The server boundary still applies its headers.
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Request-ID") == "" {
			t.Fatalf("%s: boundary headers missing", path)
		}
	}
	// Removed features: same 501 envelope as at the root, any letter case.
	for _, path := range []string{"/compat/LiveTv", "/compat/livetv/Programs", "/COMPAT/Channels/x/Items", "/compat/Dlna/x/description.xml"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			assertProblem(t, do(method, path, nil), 501, "feature_removed")
		}
	}
	// Transformation: path and query forms both answer the global 409.
	for _, target := range []string{
		"/compat/Videos/x/master.m3u8", "/compat/Videos/x/hls1/main/0.ts", "/compat/Videos/x/stream.mpd",
		"/compat/System/Info/Public?videoCodec=h264", "/compat/System/Info?maxStreamingBitrate=1", "/COMPAT/Items?TranscodingContainer=ts",
		"/compat/Videos/x/stream?static=false&api_key=" + compatNativeToken,
	} {
		assertProblem(t, do(http.MethodGet, target, compatAuth(compatNativeToken)), 409, "transcode_disabled")
	}
	// Host validation runs first.
	r := httptest.NewRequest(http.MethodGet, "http://evil.example/compat/System/Info/Public", nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	assertProblem(t, w, 400, "invalid_host")
	if f.backend.authCalls != 0 {
		t.Fatal("refused request reached the session store")
	}
	// CORS: refused with the layer's empty 403, no CORS headers.
	for _, origin := range []string{"http://localhost", "https://evil.example"} {
		header := compatAuth(compatNativeToken)
		header["Origin"] = []string{origin}
		w := do(http.MethodGet, "/compat/System/Info", header)
		if w.Code != 403 || w.Body.Len() != 0 || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("origin %s: %d %q", origin, w.Code, w.Body)
		}
	}
	// Unknown compat routes stay inside the layer: empty 404, never the frontend.
	if w := do(http.MethodGet, "/compat/Unknown", nil); w.Code != 404 || w.Body.Len() != 0 {
		t.Fatalf("unknown: %d %q", w.Code, w.Body)
	}
}

func TestCompatSessionKinds(t *testing.T) {
	f, do := compatFixture(t, true)
	if w := do(http.MethodGet, "/compat/System/Info", compatAuth(compatNativeToken)); w.Code != 200 {
		t.Fatalf("native: %d", w.Code)
	}
	for name, header := range map[string]http.Header{
		"web":           compatAuth(compatWebToken),
		"none":          nil,
		"bearer":        {"Authorization": {"Bearer " + compatNativeToken}},
		"web cookie":    {"Cookie": {sessionCookieName + "=" + compatWebToken}},
		"native cookie": {"Cookie": {sessionCookieName + "=" + compatNativeToken}},
	} {
		if w := do(http.MethodGet, "/compat/System/Info", header); w.Code != 401 || w.Body.Len() != 0 {
			t.Fatalf("%s: %d %q", name, w.Code, w.Body)
		}
	}
	if f.backend.authCalls != 2 {
		t.Fatalf("session lookups = %d, want 2 (native and web)", f.backend.authCalls)
	}
}

// G11.6: nothing configured about the host is published.
func TestCompatPublishesNoHostDetails(t *testing.T) {
	_, do := compatFixture(t, true)
	cfg := validConfig()
	for _, target := range []string{"/compat/System/Info/Public", "/compat/System/Info", "/compat/System/Ping"} {
		w := do(http.MethodGet, target, compatAuth(compatNativeToken))
		if w.Code != 200 {
			t.Fatalf("%s: %d", target, w.Code)
		}
		var text strings.Builder
		text.WriteString(w.Body.String())
		for name, values := range w.Header() {
			fmt.Fprintf(&text, "\n%s: %s", name, strings.Join(values, ","))
		}
		banned := append([]string{cfg.Listen, "127.0.0.1", "8097", "::1", "postgres", "/jelee"}, cfg.AllowedHosts...)
		for _, value := range banned {
			if strings.Contains(text.String(), value) {
				t.Fatalf("%s publishes %q", target, value)
			}
		}
	}
	if id := compat.DeriveServerID(cfg.AllowedHosts...); !strings.Contains(do(http.MethodGet, "/compat/System/Info/Public", nil).Body.String(), `"Id":"`+id+`"`) {
		t.Fatal("derived server identifier not reported")
	}
}

func TestCompatConfiguredServerID(t *testing.T) {
	cfg := validConfig()
	cfg.EnableCompat, cfg.CompatServerID = true, "fedcba9876543210fedcba9876543210"
	handler, err := New(cfg, &fakeBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost/compat/System/Info/Public", nil))
	if !strings.Contains(w.Body.String(), `"Id":"fedcba9876543210fedcba9876543210"`) {
		t.Fatalf("configured id not used: %s", w.Body)
	}
}

// Real session store: only native sessions are usable, through the same
// lookup the server uses for its own API.
func TestCompatSessionKindsPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	native, err := store.Provision(ctx, "compat-native", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	web, err := store.Provision(ctx, "compat-web", access.ClientWeb, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := leakHandler(t, store, leakConfig(t, dsn, 0))
	request := func(header http.Header, query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, compatRequest(http.MethodGet, "/compat/System/Info"+query, header))
		return w
	}
	for name, tc := range map[string]struct {
		header http.Header
		query  string
		want   int
	}{
		"native":        {compatAuth(native), "", 200},
		"native legacy": {nil, "?api_key=" + native, 200},
		"web":           {compatAuth(web), "", 401},
		"web legacy":    {nil, "?api_key=" + web, 401},
		"web cookie":    {http.Header{"Cookie": {sessionCookieName + "=" + web}}, "", 401},
		"unknown":       {compatAuth(strings.Repeat("A", 43)), "", 401},
	} {
		w := request(tc.header, tc.query)
		if w.Code != tc.want {
			t.Fatalf("%s: %d, want %d", name, w.Code, tc.want)
		}
		if tc.want == 200 && !strings.Contains(w.Body.String(), `"JeleeCapabilities":{"Transcoding":false`) {
			t.Fatalf("%s: body %s", name, w.Body)
		}
	}
	// The web session itself is still valid on the server's own API.
	r := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/users/me", nil)
	r.Header.Set("Authorization", "Bearer "+web)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("web session control: %d", w.Code)
	}
	// A revoked native session is refused by the shared lookup.
	if _, err := store.Pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=(SELECT id FROM users WHERE name='compat-native')`); err != nil {
		t.Fatal(err)
	}
	if w := request(compatAuth(native), ""); w.Code != 401 {
		t.Fatalf("revoked native: %d", w.Code)
	}
}
