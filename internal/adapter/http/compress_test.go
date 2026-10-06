package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func hasVaryAcceptEncoding(h http.Header) bool {
	for _, v := range h.Values("Vary") {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "Accept-Encoding") {
				return true
			}
		}
	}
	return false
}

func TestCompressionOfNonMediaResponses(t *testing.T) {
	handler := contractRouter(t, ReferenceConfig())
	get := func(target, acceptEncoding string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://localhost"+target, nil)
		if acceptEncoding != "" {
			r.Header.Set("Accept-Encoding", acceptEncoding)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	identity := get("/api/v1/openapi.json", "")
	if identity.Code != 200 || identity.Header().Get("Content-Encoding") != "" || !hasVaryAcceptEncoding(identity.Header()) {
		t.Fatalf("identity OpenAPI: %d %v", identity.Code, identity.Header())
	}
	compressed := get("/api/v1/openapi.json", "br;q=1, gzip;q=0.5")
	if compressed.Header().Get("Content-Encoding") != "gzip" || compressed.Header().Get("Content-Length") != "" || !hasVaryAcceptEncoding(compressed.Header()) {
		t.Fatalf("gzip OpenAPI headers: %v", compressed.Header())
	}
	if plain := gunzip(t, compressed.Body.Bytes()); !bytes.Equal(plain, identity.Body.Bytes()) || compressed.Body.Len() >= len(plain)/2 {
		t.Fatalf("compressed body differs or did not shrink: %d of %d bytes", compressed.Body.Len(), len(plain))
	}
	if compressed.Header().Get("X-Request-ID") == "" || compressed.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("boundary headers must survive compression")
	}
	for _, refused := range []string{"gzip;q=0", "identity", "*;q=0", "gzip;q=0, *"} {
		if w := get("/api/v1/openapi.json", refused); w.Header().Get("Content-Encoding") != "" {
			t.Errorf("Accept-Encoding %q must not get gzip", refused)
		}
	}
	if w := get("/api/v1/openapi.json", "*"); w.Header().Get("Content-Encoding") != "gzip" {
		t.Error("Accept-Encoding * allows gzip")
	}
	// Below the threshold the body stays as it is, still varying.
	small := get("/healthz", "gzip")
	if small.Header().Get("Content-Encoding") != "" || !hasVaryAcceptEncoding(small.Header()) || !strings.Contains(small.Body.String(), `"ok"`) {
		t.Fatalf("small body: %v %s", small.Header(), small.Body.String())
	}
	// Error envelopes are JSON too.
	if w := get("/api/v1/not-a-route", "gzip"); w.Code != 404 || w.Header().Get("Content-Encoding") != "" || !hasVaryAcceptEncoding(w.Header()) {
		t.Fatalf("small error: %d %v", w.Code, w.Header())
	}
	head := httptest.NewRequest(http.MethodHead, "http://localhost/api/v1/openapi.json", nil)
	head.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, head)
	if w.Header().Get("Content-Encoding") != "" {
		t.Fatal("HEAD is never encoded")
	}

	off := ReferenceConfig()
	off.Compression = config.CompressionConfig{Mode: "off"}
	offHandler := contractRouter(t, off)
	r := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/openapi.json", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	offHandler.ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "" || hasVaryAcceptEncoding(w.Header()) {
		t.Fatalf("compression off: %v", w.Header())
	}
	threshold := ReferenceConfig()
	threshold.Compression = config.CompressionConfig{Level: 1, MinBytes: 8}
	r = httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	contractRouter(t, threshold).ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(string(gunzip(t, w.Body.Bytes())), `"ok"`) {
		t.Fatalf("configured threshold: %v", w.Header())
	}
}

// Original media, tracks with a text type and Range answers keep their
// exact bytes whatever the client accepts.
func TestCompressionNeverTouchesMedia(t *testing.T) {
	f := newFixture(t, true, true)
	root := t.TempDir()
	original := bytes.Repeat([]byte("plain subtitle line that compresses well\n"), 200)
	if err := os.WriteFile(filepath.Join(root, "track.idx"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, contentType := range []string{"text/plain", "video/mp4", "text/vtt", "application/json"} {
		f.resolver.resolve = func(context.Context, access.Principal, string) (media.Source, error) {
			return media.Source{Root: root, RelativePath: "track.idx", ContentType: contentType, ETag: `"rev-1"`}, nil
		}
		for _, rangeHeader := range []string{"", "bytes=10-19"} {
			r := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/sources/"+sourceID+"/stream", nil)
			r.Header.Set("Authorization", "Bearer "+strings.Repeat("n", 43))
			r.Header.Set("Accept-Encoding", "gzip")
			if rangeHeader != "" {
				r.Header.Set("Range", rangeHeader)
			}
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			want := original
			if rangeHeader != "" {
				want = original[10:20]
			}
			if w.Header().Get("Content-Encoding") != "" || !bytes.Equal(w.Body.Bytes(), want) || w.Header().Get("ETag") != `"rev-1"` || w.Header().Get("Accept-Ranges") != "bytes" {
				t.Fatalf("%s range=%q: status %d headers %v", contentType, rangeHeader, w.Code, w.Header())
			}
		}
	}
}

// recordingWriter is a ResponseWriter that also implements ReaderFrom.
type recordingWriter struct {
	*httptest.ResponseRecorder
	readFrom int
}

func (w *recordingWriter) ReadFrom(src io.Reader) (int64, error) {
	w.readFrom++
	return io.Copy(w.ResponseRecorder, src)
}

func newTestCompression(minBytes int) *compression {
	return newCompression(config.CompressionConfig{MinBytes: minBytes})
}

func serveCompressed(z *compression, r *http.Request, w http.ResponseWriter, handler http.HandlerFunc) {
	wrapped, finish := z.wrap(w, r)
	handler(wrapped, r)
	finish()
}

func gzipRequest(header ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://localhost/x", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	return r
}

func TestGzipWriterDecisions(t *testing.T) {
	z := newTestCompression(16)
	body := strings.Repeat("x", 64)
	for _, tc := range []struct {
		name     string
		headers  map[string]string
		status   int
		compress bool
	}{
		{"json", map[string]string{"Content-Type": "application/json; charset=utf-8"}, 200, true},
		{"error json", map[string]string{"Content-Type": "application/json"}, 500, true},
		{"ndjson export", map[string]string{"Content-Type": "application/x-ndjson"}, 200, true},
		{"html", map[string]string{"Content-Type": "text/html; charset=utf-8"}, 200, true},
		{"png", map[string]string{"Content-Type": "image/png"}, 200, false},
		{"svg", map[string]string{"Content-Type": "image/svg+xml"}, 200, false},
		{"vtt", map[string]string{"Content-Type": "text/vtt"}, 200, false},
		{"srt", map[string]string{"Content-Type": "application/x-subrip"}, 200, false},
		{"octet", map[string]string{"Content-Type": "application/octet-stream"}, 200, false},
		{"no type", map[string]string{}, 200, false},
		{"byte ranges", map[string]string{"Content-Type": "text/plain", "Accept-Ranges": "bytes"}, 200, false},
		{"ranges none", map[string]string{"Content-Type": "text/plain", "Accept-Ranges": "none"}, 200, true},
		{"partial", map[string]string{"Content-Type": "text/plain", "Content-Range": "bytes 0-1/2"}, 206, false},
		{"encoded", map[string]string{"Content-Type": "text/plain", "Content-Encoding": "br"}, 200, false},
		{"no-transform", map[string]string{"Content-Type": "text/plain", "Cache-Control": "private, no-transform"}, 200, false},
	} {
		w := httptest.NewRecorder()
		serveCompressed(z, gzipRequest(), w, func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range tc.headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, body)
		})
		got := w.Header().Get("Content-Encoding") == "gzip"
		if got != tc.compress || w.Code != tc.status {
			t.Errorf("%s: compressed=%v status=%d", tc.name, got, w.Code)
			continue
		}
		if got && string(gunzip(t, w.Body.Bytes())) != body || !got && tc.headers["Content-Encoding"] == "" && w.Body.String() != body {
			t.Errorf("%s: body changed", tc.name)
		}
	}
	// A 304 and a 204 have no body to encode.
	for _, status := range []int{http.StatusNotModified, http.StatusNoContent} {
		w := httptest.NewRecorder()
		serveCompressed(z, gzipRequest(), w, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
		})
		if w.Code != status || w.Header().Get("Content-Encoding") != "" {
			t.Errorf("status %d: %v", status, w.Header())
		}
	}
}

// A compressed representation keeps a weak form of a strong ETag, and the
// weak validator still produces 304 through ServeContent's weak comparison.
func TestGzipWriterWeakensETagAndKeepsConditionalRequests(t *testing.T) {
	z := newTestCompression(16)
	content := strings.Repeat("static frontend asset ", 100)
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("ETag", `"asset-1"`)
		allowRangedCompression(w)
		http.ServeContent(w, r, "app.js", time.Time{}, strings.NewReader(content))
	}
	w := httptest.NewRecorder()
	serveCompressed(z, gzipRequest(), w, handler)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("ETag") != `W/"asset-1"` || w.Header().Get("Accept-Ranges") != "" || w.Header().Get("Content-Length") != "" {
		t.Fatalf("compressed asset headers: %d %v", w.Code, w.Header())
	}
	if string(gunzip(t, w.Body.Bytes())) != content {
		t.Fatal("asset body changed")
	}
	for _, validator := range []string{`W/"asset-1"`, `"asset-1"`} {
		w = httptest.NewRecorder()
		serveCompressed(z, gzipRequest("If-None-Match", validator), w, handler)
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("Content-Encoding") != "" {
			t.Fatalf("If-None-Match %s: %d %v", validator, w.Code, w.Header())
		}
	}
	// A Range request gets the identity bytes and keeps the strong ETag.
	w = httptest.NewRecorder()
	serveCompressed(z, gzipRequest("Range", "bytes=0-5"), w, handler)
	if w.Code != http.StatusPartialContent || w.Body.String() != content[:6] || w.Header().Get("ETag") != `"asset-1"` || w.Header().Get("Content-Encoding") != "" {
		t.Fatalf("range: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	// Without the frontend's opt-in a ranged resource is never compressed.
	w = httptest.NewRecorder()
	serveCompressed(z, gzipRequest(), w, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(content))
	})
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != content || w.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("ranged without opt-in: %v", w.Header())
	}
}

func TestGzipWriterStreamingAndPassthroughPaths(t *testing.T) {
	z := newTestCompression(1024)
	// A flush below the threshold starts compression: the handler streams.
	w := httptest.NewRecorder()
	serveCompressed(z, gzipRequest(), w, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, "{\"a\":1}\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, "{\"a\":2}\n")
	})
	if !w.Flushed || w.Header().Get("Content-Encoding") != "gzip" || string(gunzip(t, w.Body.Bytes())) != "{\"a\":1}\n{\"a\":2}\n" {
		t.Fatalf("streamed export: %v", w.Header())
	}
	// Passed-through bodies keep the connection's ReaderFrom (sendfile).
	rw := &recordingWriter{ResponseRecorder: httptest.NewRecorder()}
	serveCompressed(z, gzipRequest(), rw, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.(io.ReaderFrom).ReadFrom(strings.NewReader("frames"))
	})
	if rw.readFrom != 1 || rw.Body.String() != "frames" || rw.Header().Get("Vary") != "" {
		t.Fatalf("passthrough ReadFrom: calls=%d body=%q vary=%q", rw.readFrom, rw.Body.String(), rw.Header().Get("Vary"))
	}
	// A compressed body read through ReadFrom is encoded, not sent raw.
	rw = &recordingWriter{ResponseRecorder: httptest.NewRecorder()}
	large := strings.Repeat("{}", 2048)
	serveCompressed(z, gzipRequest(), rw, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.(io.ReaderFrom).ReadFrom(strings.NewReader(large))
	})
	if rw.readFrom != 0 || string(gunzip(t, rw.Body.Bytes())) != large {
		t.Fatalf("compressed ReadFrom: calls=%d", rw.readFrom)
	}
	// Nothing written: nothing to finish.
	w = httptest.NewRecorder()
	serveCompressed(z, gzipRequest(), w, func(http.ResponseWriter, *http.Request) {})
	if w.Body.Len() != 0 || w.Header().Get("Content-Encoding") != "" {
		t.Fatal("an empty handler must leave the response alone")
	}
	var off *compression
	plain, finish := off.wrap(w, gzipRequest())
	finish()
	if plain != w {
		t.Fatal("disabled compression must not wrap")
	}
}
