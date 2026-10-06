package httpapi

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// frontendCSP applies to the single-page frontend only. API responses keep
// the boundary's default-src 'none' policy. The site appearance's external
// font allowlist adds a font-src directive (frontendCSPWithFonts); custom
// CSS needs no relaxation because the web client applies it as a
// constructed stylesheet, which style-src does not govern.
const frontendCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

const (
	frontendIndex     = "index.html"
	frontendAssetsDir = "assets/"
)

// Build tools name content-addressed output like assets/index-B3x_9kQd.js.
var hashedAssetName = regexp.MustCompile(`[-.][A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$`)

// webApp serves a built frontend directory (JELEE_WEB_DIR). Every open goes
// through os.Root, which refuses absolute names, ".." and symlinks that leave
// the directory, so a request can never read outside it. The root is opened
// per request so a redeployed directory is picked up without a restart.
type webApp struct {
	dir string
	// policy supplies the Content-Security-Policy; nil uses frontendCSP.
	policy *frontendPolicy
}

func newWebApp(dir string) (*webApp, error) {
	if dir == "" {
		return nil, nil
	}
	app := &webApp{dir: dir}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("web directory cannot be opened")
	}
	defer root.Close()
	info, err := root.Stat(frontendIndex)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("web directory must contain a regular index.html")
	}
	return app, nil
}

// handles reports whether a request that matched no API route belongs to the
// frontend. Everything under /api keeps the JSON not-found answer.
func (a *webApp) handles(r *http.Request) bool {
	if a == nil || r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	return p != "/api" && !strings.HasPrefix(p, "/api/")
}

func (a *webApp) serve(w http.ResponseWriter, r *http.Request) {
	name, ok := frontendName(r.URL.Path)
	if !ok {
		WriteError(w, r, domain.ErrNotFound)
		return
	}
	root, err := os.OpenRoot(a.dir)
	if err != nil {
		WriteError(w, r, domain.ErrNotFound)
		return
	}
	defer root.Close()
	if name != "" {
		file, info, err := openRegular(root, name)
		switch {
		case err == nil:
			defer file.Close()
			a.write(w, r, name, info, file)
			return
		case !errors.Is(err, fs.ErrNotExist) || strings.HasPrefix(name, frontendAssetsDir):
			// Escapes, unreadable entries and missing build assets are plain
			// 404s; only client routes fall back to the application shell, so a
			// stale script URL is never answered with HTML.
			WriteError(w, r, domain.ErrNotFound)
			return
		}
	}
	file, info, err := openRegular(root, frontendIndex)
	if err != nil {
		WriteError(w, r, domain.ErrNotFound)
		return
	}
	defer file.Close()
	a.write(w, r, frontendIndex, info, file)
}

func (a *webApp) write(w http.ResponseWriter, r *http.Request, name string, info fs.FileInfo, file *os.File) {
	header := w.Header()
	csp := frontendCSP
	if a.policy != nil {
		csp = a.policy.header(r.Context())
	}
	header.Set("Content-Security-Policy", csp)
	switch {
	case name == frontendIndex:
		header.Set("Cache-Control", "no-store")
	case strings.HasPrefix(name, frontendAssetsDir) && hashedAssetName.MatchString(name):
		header.Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		header.Set("Cache-Control", "no-cache")
	}
	if contentType, ok := frontendTypes[strings.ToLower(path.Ext(name))]; ok {
		// ServeContent would otherwise consult the OS MIME table, which differs
		// by platform (Windows maps .js to application/javascript from the
		// registry); with nosniff the browser trusts this header for modules.
		header.Set("Content-Type", contentType)
	}
	// Frontend files answer Range like any static file; requests without
	// Range may still be compressed (G11.7).
	allowRangedCompression(w)
	http.ServeContent(w, r, name, info.ModTime(), file)
}

// frontendTypes fixes the media types of the files a frontend build ships.
// Other extensions fall back to ServeContent's detection.
var frontendTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".webp":        "image/webp",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

// openRegular opens name inside root and accepts only regular files. A
// directory is reported as missing so it falls back like any client route.
func openRegular(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if info.IsDir() {
		file.Close()
		return nil, nil, fs.ErrNotExist
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, fs.ErrPermission
	}
	return file, info, nil
}

// frontendName maps a URL path to a slash-separated name inside the web
// directory. Empty means the application shell. Dot segments, empty segments,
// hidden names, backslashes and control bytes are refused outright instead of
// being cleaned into some other name.
func frontendName(p string) (string, bool) {
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	name := strings.TrimPrefix(p, "/")
	if name == "" {
		return "", true
	}
	name = strings.TrimSuffix(name, "/")
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") {
			return "", false
		}
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c == 0x7f || c == '\\' || c == ':' {
			return "", false
		}
	}
	return name, true
}
