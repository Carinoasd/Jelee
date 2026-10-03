package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

const (
	apiCSP     = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'"
	indexBody  = "<!doctype html><title>Jelee</title><div id=app></div>"
	secretBody = "outside-secret-marker"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// webFixture builds base/dist with a frontend and base/outside with a secret,
// plus symlinks inside dist that point out of it.
func webFixture(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	dist := filepath.Join(base, "dist")
	writeFile(t, filepath.Join(dist, "index.html"), indexBody)
	writeFile(t, filepath.Join(dist, "assets", "index-B3x_9kQd.js"), "console.log(1)")
	writeFile(t, filepath.Join(dist, "assets", "index-Cf8a21Zq.css"), "body{}")
	writeFile(t, filepath.Join(dist, "assets", "logo.svg"), "<svg/>")
	writeFile(t, filepath.Join(dist, "robots.txt"), "User-agent: *")
	writeFile(t, filepath.Join(dist, ".env"), secretBody)
	writeFile(t, filepath.Join(base, "outside", "secret.txt"), secretBody)
	for name, target := range map[string]string{
		"escape.txt":      filepath.Join(base, "outside", "secret.txt"),
		"escape-rel.txt":  "../outside/secret.txt",
		"linkdir":         filepath.Join(base, "outside"),
		"assets/leak.js":  filepath.Join(base, "outside", "secret.txt"),
		"inside-link.txt": "robots.txt",
	} {
		if err := os.Symlink(target, filepath.Join(dist, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dist
}

func webHandler(t *testing.T, dir string) http.Handler {
	t.Helper()
	cfg := validConfig()
	cfg.WebDir = dir
	handler, err := New(cfg, &fakeBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func webGet(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost/", nil)
	r.URL.Path, r.URL.RawPath = target, ""
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestWebAppServesShellAssetsAndFrontendCSP(t *testing.T) {
	h := webHandler(t, webFixture(t))
	for _, test := range []struct {
		path, body, cache, contentType string
	}{
		{"/", indexBody, "no-store", "text/html"},
		{"/index.html", indexBody, "no-store", "text/html"},
		{"/library/55555555-5555-4555-8555-555555555555", indexBody, "no-store", "text/html"},
		{"/settings/profile/", indexBody, "no-store", "text/html"},
		{"/assets", indexBody, "no-store", "text/html"},
		{"/apix", indexBody, "no-store", "text/html"},
		{"/assets/index-B3x_9kQd.js", "console.log(1)", "public, max-age=31536000, immutable", "text/javascript"},
		{"/assets/index-Cf8a21Zq.css", "body{}", "public, max-age=31536000, immutable", "text/css"},
		{"/assets/logo.svg", "<svg/>", "no-cache", "image/svg+xml"},
		{"/robots.txt", "User-agent: *", "no-cache", "text/plain"},
		{"/inside-link.txt", "User-agent: *", "no-cache", "text/plain"},
	} {
		w := webGet(h, "GET", test.path)
		if w.Code != 200 || w.Body.String() != test.body || w.Header().Get("Cache-Control") != test.cache || !strings.HasPrefix(w.Header().Get("Content-Type"), test.contentType) {
			t.Errorf("%s: %d %q cache=%q type=%q", test.path, w.Code, w.Body.String(), w.Header().Get("Cache-Control"), w.Header().Get("Content-Type"))
		}
		if w.Header().Get("Content-Security-Policy") != frontendCSP || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: frontend headers %v", test.path, w.Header())
		}
	}
	if w := webGet(h, "HEAD", "/"); w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Security-Policy") != frontendCSP {
		t.Fatalf("HEAD shell: %d %v", w.Code, w.Header())
	}
	// API responses keep the strict policy even with a frontend configured.
	for _, path := range []string{"/healthz", "/api/v1/system", "/api/v1/openapi.json"} {
		if w := webGet(h, "GET", path); w.Code != 200 || w.Header().Get("Content-Security-Policy") != apiCSP || strings.Contains(w.Body.String(), indexBody) {
			t.Errorf("%s: API CSP changed: %d %q", path, w.Code, w.Header().Get("Content-Security-Policy"))
		}
	}
}

func TestWebAppNeverFallsBackForAPIOrUnsafeMethods(t *testing.T) {
	h := webHandler(t, webFixture(t))
	for _, test := range []struct{ method, path string }{
		{"GET", "/api"}, {"GET", "/api/"}, {"GET", "/api/v1/unknown"}, {"GET", "/api/v2/items"}, {"HEAD", "/api/v1/unknown"},
		{"POST", "/library"}, {"PUT", "/"}, {"DELETE", "/settings"},
		{"GET", "/assets/index-Missing1.js"}, {"GET", "/assets/missing.css"},
	} {
		w := webGet(h, test.method, test.path)
		if w.Code != 404 || strings.Contains(w.Body.String(), indexBody) || w.Header().Get("Content-Security-Policy") != apiCSP {
			t.Errorf("%s %s: %d %q csp=%q", test.method, test.path, w.Code, w.Body.String(), w.Header().Get("Content-Security-Policy"))
		}
		if test.method != "HEAD" && !strings.Contains(w.Body.String(), `"not_found"`) {
			t.Errorf("%s %s: not the JSON not-found envelope", test.method, test.path)
		}
	}
}

func TestWebAppRejectsTraversalAndSymlinkEscape(t *testing.T) {
	h := webHandler(t, webFixture(t))
	for _, path := range []string{
		"/../outside/secret.txt", "/assets/../../outside/secret.txt", "/./index.html", "/assets/./index-B3x_9kQd.js", "/a//b",
		"/escape.txt", "/escape-rel.txt", "/linkdir/secret.txt", "/linkdir", "/assets/leak.js",
		"/.env", "/assets\\..\\..\\outside\\secret.txt", "/C:/outside/secret.txt", "/index.html\x00.js",
	} {
		w := webGet(h, "GET", path)
		if w.Code != 404 || strings.Contains(w.Body.String(), secretBody) || strings.Contains(w.Body.String(), indexBody) {
			t.Errorf("%q: %d %q", path, w.Code, w.Body.String())
		}
	}
	// Percent-encoded dot segments arrive decoded in URL.Path and are refused too.
	r := httptest.NewRequest("GET", "http://localhost/%2e%2e/outside/secret.txt", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || strings.Contains(w.Body.String(), secretBody) {
		t.Fatalf("encoded traversal: %d %q", w.Code, w.Body.String())
	}
}

func TestWebAppDisabledAndInvalidDirectories(t *testing.T) {
	h := webHandler(t, "")
	for _, path := range []string{"/", "/index.html", "/library"} {
		if w := webGet(h, "GET", path); w.Code != 404 || !strings.Contains(w.Body.String(), `"not_found"`) || w.Header().Get("Content-Security-Policy") != apiCSP {
			t.Errorf("%s without JELEE_WEB_DIR: %d %q", path, w.Code, w.Body.String())
		}
	}
	empty := t.TempDir()
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(webFixture(t), "index.html"), filepath.Join(linked, "index.html")); err != nil {
		t.Fatal(err)
	}
	dirIndex := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirIndex, "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"missing": filepath.Join(empty, "nope"), "no-index": empty, "escaping-index": linked, "directory-index": dirIndex} {
		cfg := validConfig()
		cfg.WebDir = dir
		if _, err := New(cfg, &fakeBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
			t.Errorf("%s: constructor accepted an unusable web directory", name)
		}
	}
}

func TestWebDirConfiguration(t *testing.T) {
	base := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	lookup := func(extra map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			if v, ok := extra[key]; ok {
				return v, true
			}
			v, ok := base[key]
			return v, ok
		}
	}
	cfg, err := config.LoadWith(lookup(nil))
	if err != nil || cfg.WebDir != "" {
		t.Fatalf("default web dir: %q %v", cfg.WebDir, err)
	}
	cfg, err = config.LoadWith(lookup(map[string]string{"JELEE_WEB_DIR": "/srv/jelee/web/dist"}))
	if err != nil || cfg.WebDir != "/srv/jelee/web/dist" {
		t.Fatalf("absolute web dir: %q %v", cfg.WebDir, err)
	}
	for _, bad := range []string{"web/dist", "./dist", "/srv/../etc", "/srv/web/", "/srv/a\x00b"} {
		if _, err = config.LoadWith(lookup(map[string]string{"JELEE_WEB_DIR": bad})); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFrontendNameAndHashedAssets(t *testing.T) {
	for path, want := range map[string]string{"/": "", "/a": "a", "/a/b/": "a/b", "/assets/x-12345678.js": "assets/x-12345678.js"} {
		if got, ok := frontendName(path); !ok || got != want {
			t.Errorf("%q -> %q %v", path, got, ok)
		}
	}
	for _, path := range []string{"", "a", "/..", "/a/../b", "/.git/config", "//a", "/a\\b", "/a\x01", "/c:"} {
		if _, ok := frontendName(path); ok {
			t.Errorf("%q accepted", path)
		}
	}
	for name, hashed := range map[string]bool{"assets/index-B3x_9kQd.js": true, "assets/chunk.DX9a_b-c.js": true, "assets/logo.svg": false, "assets/app-1.js": false} {
		if hashedAssetName.MatchString(name) != hashed {
			t.Errorf("%s hashed=%v", name, !hashed)
		}
	}
}
