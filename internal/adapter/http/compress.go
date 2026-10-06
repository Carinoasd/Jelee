package httpapi

import (
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// Response compression (G11.7). Only non-media responses are compressed:
// the decision is made when the status is written, from a whitelist of
// content types, and every byte addressable response (Range requests,
// Content-Range, Accept-Ranges: bytes) is passed through untouched, so
// original media, external and extracted tracks, attachments and images
// keep their exact bytes, Range semantics and the zero-copy path. Bodies
// below the threshold are sent as they are. A compressed response drops
// Content-Length and Accept-Ranges and weakens a strong ETag (the encoded
// bytes differ from the identity representation; weak comparison still
// matches If-None-Match). Eligible responses carry Vary: Accept-Encoding
// whether or not they end up compressed.

// compressibleTypes are the media types that may be compressed. Subtitle,
// image, font, audio and video types are deliberately absent.
var compressibleTypes = map[string]bool{
	"application/json":          true,
	"application/problem+json":  true,
	"application/x-ndjson":      true,
	"application/manifest+json": true,
	"application/javascript":    true,
	"text/javascript":           true,
	"text/html":                 true,
	"text/css":                  true,
	"text/plain":                true,
	"text/csv":                  true,
}

type compression struct {
	minBytes int
	writers  sync.Pool
}

// newCompression is nil when compression is off.
func newCompression(c config.CompressionConfig) *compression {
	if !c.Enabled() {
		return nil
	}
	level := c.GzipLevel()
	z := &compression{minBytes: c.Threshold()}
	z.writers.New = func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, level)
		return w
	}
	return z
}

// wrap returns the writer the handlers use and the function that completes
// the response; it must run after the handler returned.
func (z *compression) wrap(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, func()) {
	if z == nil {
		return w, func() {}
	}
	g := &gzipResponseWriter{ResponseWriter: w, z: z, accepts: r.Method != http.MethodHead && acceptsGzip(r.Header.Values("Accept-Encoding")), ranged: r.Header.Get("Range") != ""}
	return g, g.finish
}

// acceptsGzip reports whether Accept-Encoding allows gzip (an explicit gzip,
// or * that gzip does not override, with a nonzero quality).
func acceptsGzip(values []string) bool {
	gzipQ, starQ := -1.0, -1.0
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			q := 1.0
			if key, raw, ok := strings.Cut(strings.TrimSpace(params), "="); ok && strings.EqualFold(strings.TrimSpace(key), "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
				if err != nil {
					continue
				}
				q = parsed
			}
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "gzip", "x-gzip":
				gzipQ = q
			case "*":
				starQ = q
			}
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return starQ > 0
}

type gzipState int

const (
	gzipUndecided gzipState = iota
	gzipPassthrough
	gzipBuffering
	gzipCompressing
)

type gzipResponseWriter struct {
	http.ResponseWriter
	z       *compression
	accepts bool
	ranged  bool
	// rangedAllowed lets a handler whose resource also answers Range
	// (the static frontend) be compressed when the request has no Range.
	rangedAllowed bool
	state         gzipState
	status        int
	buffer        []byte
	gz            *gzip.Writer
}

// allowRangedCompression marks a response that advertises Accept-Ranges as
// still compressible for requests without Range. Media handlers never call
// it.
func allowRangedCompression(w http.ResponseWriter) {
	for {
		if g, ok := w.(*gzipResponseWriter); ok {
			g.rangedAllowed = true
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

// Unwrap lets http.ResponseController reach deadlines and hijacking.
func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// eligible decides from the response headers whether the body is a
// compressible non-media representation.
func (g *gzipResponseWriter) eligible(status int) bool {
	h := g.Header()
	if status < 200 || status == http.StatusNoContent || status == http.StatusPartialContent || status == http.StatusNotModified {
		return false
	}
	if h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" || strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-transform") {
		return false
	}
	if ranges := h.Get("Accept-Ranges"); ranges != "" && ranges != "none" && !g.rangedAllowed {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && compressibleTypes[mediaType]
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		g.ResponseWriter.WriteHeader(status)
		return
	}
	if g.state != gzipUndecided {
		if g.state == gzipPassthrough {
			g.ResponseWriter.WriteHeader(status)
		}
		return
	}
	if !g.eligible(status) {
		g.state = gzipPassthrough
		g.ResponseWriter.WriteHeader(status)
		return
	}
	g.Header().Add("Vary", "Accept-Encoding")
	if !g.accepts || g.ranged {
		g.state = gzipPassthrough
		g.ResponseWriter.WriteHeader(status)
		return
	}
	g.state, g.status = gzipBuffering, status
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if g.state == gzipUndecided {
		g.WriteHeader(http.StatusOK)
	}
	switch g.state {
	case gzipPassthrough:
		return g.ResponseWriter.Write(p)
	case gzipBuffering:
		if len(g.buffer)+len(p) < g.z.minBytes {
			g.buffer = append(g.buffer, p...)
			return len(p), nil
		}
		if err := g.startCompression(); err != nil {
			return 0, err
		}
	}
	return g.gz.Write(p)
}

// ReadFrom keeps net/http's zero-copy path for passed-through responses.
func (g *gzipResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if g.state == gzipUndecided {
		g.WriteHeader(http.StatusOK)
	}
	if g.state == gzipPassthrough {
		if to, ok := g.ResponseWriter.(io.ReaderFrom); ok {
			return to.ReadFrom(src)
		}
		return io.Copy(writerOnly{g.ResponseWriter}, src)
	}
	return io.Copy(writerOnly{g}, src)
}

// writerOnly hides ReadFrom so io.Copy cannot recurse into it.
type writerOnly struct{ io.Writer }

func (g *gzipResponseWriter) startCompression() error {
	h := g.Header()
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	if etag := h.Get("ETag"); strings.HasPrefix(etag, `"`) {
		h.Set("ETag", "W/"+etag)
	}
	g.ResponseWriter.WriteHeader(g.status)
	g.gz = g.z.writers.Get().(*gzip.Writer)
	g.gz.Reset(g.ResponseWriter)
	g.state = gzipCompressing
	buffered := g.buffer
	g.buffer = nil
	if len(buffered) > 0 {
		if _, err := g.gz.Write(buffered); err != nil {
			return err
		}
	}
	return nil
}

// FlushError sends what was written so far. A flush while still below the
// threshold means the handler streams, so compression starts now.
func (g *gzipResponseWriter) FlushError() error {
	if g.state == gzipUndecided {
		g.WriteHeader(http.StatusOK)
	}
	if g.state == gzipBuffering {
		if err := g.startCompression(); err != nil {
			return err
		}
	}
	if g.state == gzipCompressing {
		if err := g.gz.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(g.ResponseWriter).Flush()
}

func (g *gzipResponseWriter) Flush() { _ = g.FlushError() }

// finish writes a body that stayed below the threshold as it is, or closes
// the gzip stream.
func (g *gzipResponseWriter) finish() {
	switch g.state {
	case gzipBuffering:
		g.state = gzipPassthrough
		g.ResponseWriter.WriteHeader(g.status)
		if len(g.buffer) > 0 {
			_, _ = g.ResponseWriter.Write(g.buffer)
		}
		g.buffer = nil
	case gzipCompressing:
		_ = g.gz.Close()
		g.gz.Reset(io.Discard)
		g.z.writers.Put(g.gz)
		g.gz = nil
		g.state = gzipPassthrough
	}
}
