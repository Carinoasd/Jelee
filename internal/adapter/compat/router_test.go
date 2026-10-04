package compat

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/golden files")

const testServerID = "0123456789abcdef0123456789abcdef"

var (
	nativeToken = tokA
	webToken    = tokB
)

type harness struct {
	handler    http.Handler
	authCalls  int
	rejections int
	authErr    error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{}
	handler, err := NewRouter(Options{
		Authenticate: func(_ context.Context, token string) (access.Principal, error) {
			h.authCalls++
			if h.authErr != nil {
				return access.Principal{}, h.authErr
			}
			switch token {
			case nativeToken:
				return access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientNative}, nil
			case webToken:
				return access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientWeb}, nil
			}
			return access.Principal{}, domain.ErrUnauthenticated
		},
		WriteRejection: func(w http.ResponseWriter, _ *http.Request, err error) {
			h.rejections++
			if !errors.Is(err, media.ErrTranscodeDisabled) {
				t.Errorf("unexpected rejection %v", err)
			}
			w.WriteHeader(http.StatusConflict)
		},
		ServerID: testServerID,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.handler = handler
	return h
}

func (h *harness) do(method, target string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+target, nil)
	for k, vs := range header {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func authHeader(token string) http.Header { return hdr("Authorization", primary(`Token="`+token+`"`)) }

func TestNewRouterRequiresOptions(t *testing.T) {
	good := Options{Authenticate: func(context.Context, string) (access.Principal, error) { return access.Principal{}, nil }, WriteRejection: func(http.ResponseWriter, *http.Request, error) {}, ServerID: testServerID, Timeout: time.Second}
	for name, change := range map[string]func(*Options){
		"authenticator": func(o *Options) { o.Authenticate = nil },
		"rejection":     func(o *Options) { o.WriteRejection = nil },
		"timeout":       func(o *Options) { o.Timeout = 0 },
		"id empty":      func(o *Options) { o.ServerID = "" },
		"id upper":      func(o *Options) { o.ServerID = strings.ToUpper(testServerID) },
		"id dashed":     func(o *Options) { o.ServerID = "01234567-89ab-cdef-0123-456789abcdef" },
		"id nil":        func(o *Options) { o.ServerID = strings.Repeat("0", 32) },
	} {
		opts := good
		change(&opts)
		if r, err := NewRouter(opts); err == nil || r != nil {
			t.Fatalf("%s: invalid options accepted", name)
		}
	}
	if _, err := NewRouter(good); err != nil {
		t.Fatal(err)
	}
}

func TestDeriveServerID(t *testing.T) {
	a := DeriveServerID("localhost", "media.example")
	if !validServerID(a) || a != DeriveServerID("media.example", "localhost") {
		t.Fatalf("unstable or invalid id %q", a)
	}
	if a == DeriveServerID("localhost") || strings.Contains(a, "localhost") {
		t.Fatal("id does not depend on its inputs or leaks them")
	}
}

func TestHasPrefix(t *testing.T) {
	for path, want := range map[string]bool{
		"/compat": true, "/compat/": true, "/compat/System/Info": true, "/COMPAT/x": true, "/CoMpAt": true,
		"/compatx": false, "/compat-x/y": false, "/api/compat": false, "compat/x": false, "/": false, "": false, "/compa": false,
	} {
		if HasPrefix(path) != want {
			t.Fatalf("HasPrefix(%q) != %v", path, want)
		}
	}
}

func TestRoutesAreCaseInsensitive(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{
		"/compat/System/Info/Public", "/compat/system/info/public", "/COMPAT/SYSTEM/INFO/PUBLIC",
		"/Compat/sYsTeM/iNfO/pUbLiC", "/compat/System/Info/Public/",
	} {
		if w := h.do(http.MethodGet, path, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), testServerID) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if w := h.do(method, "/compat/system/PING", nil); w.Code != http.StatusOK || w.Body.String() != `"Jelee Server"` {
			t.Fatalf("%s ping: %d %q", method, w.Code, w.Body)
		}
	}
	if w := h.do(http.MethodGet, "/compat/SYSTEM/info", authHeader(nativeToken)); w.Code != http.StatusOK {
		t.Fatalf("authenticated route: %d", w.Code)
	}
}

func TestUnmatchedRoutes(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{
		"/compat", "/compat/", "/compat/System", "/compat/System//Info/Public", "/compat//System/Info/Public",
		"/compat/System/Info/Public//", "/compat/System/Info/Publicx", "/compat/System/Info/Public/x",
		"/compat/System%2FInfo/Public", "/compat/System%2fInfo%2FPublic", "/compat/Systém/Info/Public", "/other/System/Info/Public",
	} {
		w := h.do(http.MethodGet, path, nil)
		if w.Code != http.StatusNotFound || w.Body.Len() != 0 {
			t.Fatalf("%s: %d %q", path, w.Code, w.Body)
		}
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if w := h.do(method, "/compat/System/Info/Public", nil); w.Code != http.StatusMethodNotAllowed || w.Body.Len() != 0 {
			t.Fatalf("%s: %d %q", method, w.Code, w.Body)
		}
	}
	if w := h.do(http.MethodPost, "/compat/system/info", authHeader(nativeToken)); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST info: %d", w.Code)
	}
	if h.authCalls != 0 {
		t.Fatal("unmatched route authenticated")
	}
}

func TestCanonicalPrefersLiterals(t *testing.T) {
	rt := &router{}
	for _, p := range []string{"/Items/{id}", "/Items/Latest", "/Items/{id}/Images/{type}"} {
		rt.patterns = append(rt.patterns, strings.Split(p, "/"))
	}
	for in, want := range map[string]string{
		"/items/latest":         "/Items/Latest",
		"/ITEMS/AbCd":           "/Items/AbCd",
		"/items/AbCd/images/Pr": "/Items/AbCd/Images/Pr",
		"/items/":               "/items",
		"/items":                "/items",
		"":                      "/",
		"/items//images/x":      "/items//images/x",
	} {
		if got := rt.canonical(in); got != want {
			t.Fatalf("canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCORSIsRefused(t *testing.T) {
	h := newHarness(t)
	for _, origin := range []string{"https://evil.example", "http://localhost", "null", ""} {
		for _, req := range []struct {
			method, path string
			header       http.Header
		}{
			{http.MethodGet, "/compat/System/Info/Public", nil},
			{http.MethodGet, "/compat/System/Info", authHeader(nativeToken)},
			{http.MethodPost, "/compat/System/Ping", nil},
			{http.MethodOptions, "/compat/System/Info", hdr("Access-Control-Request-Method", "GET", "Access-Control-Request-Headers", "authorization")},
			{http.MethodGet, "/compat/Unknown", nil},
		} {
			header := req.header.Clone()
			if header == nil {
				header = http.Header{}
			}
			header["Origin"] = []string{origin}
			w := h.do(req.method, req.path, header)
			if w.Code != http.StatusForbidden || w.Body.Len() != 0 {
				t.Fatalf("%s %s origin %q: %d %q", req.method, req.path, origin, w.Code, w.Body)
			}
			for name := range w.Header() {
				if strings.HasPrefix(strings.ToLower(name), "access-control-") {
					t.Fatalf("CORS header %s sent", name)
				}
			}
		}
	}
	if h.authCalls != 0 {
		t.Fatal("CORS request reached session lookup")
	}
	// Control: the same requests without Origin are served.
	if w := h.do(http.MethodGet, "/compat/System/Info", authHeader(nativeToken)); w.Code != http.StatusOK {
		t.Fatalf("control: %d", w.Code)
	}
}

func TestTransformationRequestsAreRejected(t *testing.T) {
	h := newHarness(t)
	for _, target := range []string{
		"/compat/System/Info/Public?videoCodec=h264",
		"/compat/System/Ping?MaxStreamingBitrate=1000",
		"/compat/system/info?TranscodingProtocol=hls",
		"/compat/System/Info/Public?static=false",
		"/compat/Videos/x/master.m3u8",
		"/compat/Videos/x/hls/0.ts",
		"/compat/unknown?segmentLength=3",
	} {
		before := h.rejections
		if w := h.do(http.MethodGet, target, authHeader(nativeToken)); w.Code != http.StatusConflict || h.rejections != before+1 {
			t.Fatalf("%s: %d", target, w.Code)
		}
	}
	// JSON bodies are inspected too.
	r := httptest.NewRequest(http.MethodPost, "http://localhost/compat/System/Ping", strings.NewReader(`{"EnableTranscoding":true}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("body transformation: %d", w.Code)
	}
	if h.authCalls != 0 {
		t.Fatal("transformation request reached session lookup")
	}
	// Malformed queries and unsupported bodies use the generic error form.
	if w := h.do(http.MethodGet, "/compat/System/Info/Public?a=%zz", nil); w.Code != http.StatusBadRequest || w.Body.String() != genericError {
		t.Fatalf("malformed query: %d %q", w.Code, w.Body)
	}
	r = httptest.NewRequest(http.MethodPost, "http://localhost/compat/System/Ping", strings.NewReader("x"))
	r.Header.Set("Content-Type", "text/plain")
	w = httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType || w.Body.Len() != 0 {
		t.Fatalf("unsupported body: %d", w.Code)
	}
}

func TestAuthentication(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		header http.Header
		query  string
		want   int
	}{
		"none":                {nil, "", 401},
		"native header":       {authHeader(nativeToken), "", 200},
		"native legacy token": {hdr(headerLegacyToken, nativeToken), "", 200},
		"native query":        {nil, "?ApiKey=" + nativeToken, 200},
		"native legacy query": {nil, "?api_key=" + nativeToken, 200},
		"web header":          {authHeader(webToken), "", 401},
		"web query":           {nil, "?api_key=" + webToken, 401},
		"unknown":             {authHeader(tokC), "", 401},
		"malformed token":     {authHeader("short"), "", 401},
		"duplicate auth":      {hdr("Authorization", primary(`Token="`+nativeToken+`"`), "Authorization", primary(`Token="`+nativeToken+`"`)), "", 401},
		"bearer":              {hdr("Authorization", "Bearer "+nativeToken), "", 401},
		"cookie":              {hdr("Cookie", "jelee_session="+nativeToken), "", 401},
	} {
		w := h.do(http.MethodGet, "/compat/System/Info"+tc.query, tc.header)
		if w.Code != tc.want {
			t.Fatalf("%s: %d, want %d", name, w.Code, tc.want)
		}
		if tc.want != 200 && w.Body.Len() != 0 {
			t.Fatalf("%s: error body %q", name, w.Body)
		}
	}
	h.authErr = domain.ErrDatabase
	if w := h.do(http.MethodGet, "/compat/System/Info", authHeader(nativeToken)); w.Code != http.StatusServiceUnavailable || w.Body.Len() != 0 {
		t.Fatalf("database: %d", w.Code)
	}
	h.authErr = errors.New("boom /var/lib/jelee 10.0.0.5")
	if w := h.do(http.MethodGet, "/compat/System/Info", authHeader(nativeToken)); w.Code != http.StatusInternalServerError || w.Body.String() != genericError {
		t.Fatalf("internal: %d %q", w.Code, w.Body)
	}
	// Public routes never consult the session store, even with a bad token.
	h.authErr = nil
	calls := h.authCalls
	if w := h.do(http.MethodGet, "/compat/System/Info/Public", authHeader("short")); w.Code != 200 || h.authCalls != calls {
		t.Fatalf("public route: %d", w.Code)
	}
}

func TestPrincipalReachesHandler(t *testing.T) {
	rt := &router{opts: Options{Timeout: time.Second, Authenticate: func(context.Context, string) (access.Principal, error) {
		return access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientNative}, nil
	}}}
	var got access.Principal
	handler := rt.authenticate(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got, _ = access.PrincipalFromContext(r.Context()) }))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", primary(`Token="`+nativeToken+`"`))
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if got.UserID != "u" || got.Kind != access.ClientNative {
		t.Fatalf("principal %+v", got)
	}
}

func TestGoldenResponses(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name, method, path string
		header             http.Header
	}{
		{"system_info_public.json", http.MethodGet, "/compat/System/Info/Public", nil},
		{"system_info.json", http.MethodGet, "/compat/System/Info", authHeader(nativeToken)},
		{"system_ping.json", http.MethodGet, "/compat/System/Ping", nil},
		{"system_ping.json", http.MethodPost, "/compat/System/Ping", nil},
	} {
		w := h.do(tc.method, tc.path, tc.header)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Header().Get("Content-Type"))
		}
		path := filepath.Join("testdata", "golden", tc.name)
		if *updateGolden {
			if err := os.WriteFile(path, w.Body.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(w.Body.Bytes(), want) {
			t.Fatalf("%s %s differs from %s:\n got %s\nwant %s", tc.method, tc.path, path, w.Body, want)
		}
	}
}

var (
	ipv4Pattern    = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)
	ipv6Pattern    = regexp.MustCompile(`(?i)\b[0-9a-f]{0,4}(:[0-9a-f]{0,4}){2,7}\b`)
	absPathPattern = regexp.MustCompile(`(^|["\s=])(/[A-Za-z0-9._-]+)+|[A-Za-z]:\\`)
)

func TestResponsesPublishNoAddressesOrPaths(t *testing.T) {
	h := newHarness(t)
	for _, target := range []string{"/compat/System/Info/Public", "/compat/System/Info", "/compat/System/Ping"} {
		w := h.do(http.MethodGet, target, authHeader(nativeToken))
		body := w.Body.String()
		for _, pattern := range []*regexp.Regexp{ipv4Pattern, ipv6Pattern, absPathPattern} {
			if m := pattern.FindString(body); m != "" {
				t.Fatalf("%s publishes %q", target, m)
			}
		}
		for _, banned := range []string{"localhost", "LocalAddress", "Path\"", "PackageName", "Architecture", "http:", "https:"} {
			if strings.Contains(body, banned) {
				t.Fatalf("%s publishes %q", target, banned)
			}
		}
	}
}

func TestSystemInfoDeclaresNoTransformation(t *testing.T) {
	h := newHarness(t)
	body := h.do(http.MethodGet, "/compat/System/Info", authHeader(nativeToken)).Body.String()
	for _, want := range []string{`"JeleeCapabilities":{"Transcoding":false,"Remux":false,"Hls":false,"Dash":false,"LiveTv":false,"Channels":false,"Dlna":false,"Downloads":false}`, `"EncoderLocation":"NotFound"`, `"CanSelfRestart":false`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, ":true") && !strings.Contains(body, `"StartupWizardCompleted":true`) || strings.Count(body, ":true") != 1 {
		t.Fatalf("only StartupWizardCompleted may be true: %s", body)
	}
}

// The router must expose its routes to chi.Walk so the server's route
// classification tests see them under the mount point.
func TestRoutesAreWalkable(t *testing.T) {
	h := newHarness(t)
	seen := map[string]bool{}
	if err := chi.Walk(h.handler.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"GET /System/Info/Public", "GET /System/Info", "GET /System/Ping", "POST /System/Ping"} {
		if !seen[route] {
			t.Fatalf("route %s not walkable: %v", route, seen)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("unexpected routes %v", seen)
	}
}
