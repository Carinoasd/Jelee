package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/go-chi/chi/v5"
)

// Developer mode request and response body logging (G45.5,
// debug_body_logging). Only JSON bodies are captured, at most devBodyLimit
// bytes each; values under secret-looking keys are replaced before anything
// is logged, and media or other binary bodies are reported by size only.

const devBodyLimit = 4 << 10

// devRedactValue masks secrets in a decoded body with the shared developer
// mode rules (logging.MaskSecrets: secret keys, TOTP codes, password hashes,
// otpauth URIs, credentials in URLs). The log router masks again.
func devRedactValue(v any) any { return logging.MaskSecrets(v) }

func devJSONType(contentType string) bool {
	media, _, err := mime.ParseMediaType(contentType)
	return err == nil && (media == "application/json" || strings.HasSuffix(media, "+json"))
}

// devBodySummary renders one captured body for the log. complete is false
// when the body exceeded the capture limit.
func devBodySummary(contentType string, data []byte, size int64, complete bool) string {
	switch {
	case size == 0:
		return ""
	case !devJSONType(contentType):
		return "[" + strconv.FormatInt(size, 10) + " bytes, not logged]"
	case !complete:
		return "[" + strconv.FormatInt(size, 10) + " bytes JSON exceeds the capture limit, not logged]"
	}
	var v any
	if json.Unmarshal(data, &v) != nil {
		return "[" + strconv.FormatInt(size, 10) + " bytes, invalid JSON, not logged]"
	}
	out, err := json.Marshal(devRedactValue(v))
	if err != nil {
		return "[unrenderable]"
	}
	return string(out)
}

// devBodyRecorder captures the start of a response body.
type devBodyRecorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
	size   int64
}

func (d *devBodyRecorder) WriteHeader(status int) {
	if d.status == 0 {
		d.status = status
	}
	d.ResponseWriter.WriteHeader(status)
}

func (d *devBodyRecorder) Write(p []byte) (int, error) {
	if d.status == 0 {
		d.status = http.StatusOK
	}
	if room := devBodyLimit + 1 - d.buf.Len(); room > 0 && devJSONType(d.Header().Get("Content-Type")) {
		d.buf.Write(p[:min(room, len(p))])
	}
	n, err := d.ResponseWriter.Write(p)
	d.size += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach deadlines and flushing.
func (d *devBodyRecorder) Unwrap() http.ResponseWriter { return d.ResponseWriter }

// devCountingReader counts what the handler actually read.
type devCountingReader struct {
	io.Reader
	n int64
}

func (c *devCountingReader) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.n += int64(n)
	return n, err
}

func (s *Server) logBodies(next http.Handler, w http.ResponseWriter, r *http.Request) {
	requestType := r.Header.Get("Content-Type")
	var captured []byte
	counter := &devCountingReader{}
	if r.Body != nil && r.Body != http.NoBody {
		if devJSONType(requestType) {
			captured, _ = io.ReadAll(io.LimitReader(r.Body, devBodyLimit+1))
			counter.Reader = io.MultiReader(bytes.NewReader(captured), r.Body)
		} else {
			counter.Reader = r.Body
		}
		body := r.Body
		r.Body = struct {
			io.Reader
			io.Closer
		}{counter, body}
	}
	rec := &devBodyRecorder{ResponseWriter: w}
	next.ServeHTTP(rec, r)
	route := ""
	if rc := chi.RouteContext(r.Context()); rc != nil {
		route = rc.RoutePattern()
	}
	s.logger.InfoContext(r.Context(), "developer mode body log", "component", "http", "code", "devmode_body_log", "requestId", w.Header().Get("X-Request-ID"),
		"method", logging.SafeMethod(r.Method), "route", route, "status", rec.status,
		"requestBody", logging.DeveloperBody(devBodySummary(requestType, captured, max(counter.n, int64(len(captured))), len(captured) <= devBodyLimit)),
		"responseBody", logging.DeveloperBody(devBodySummary(rec.Header().Get("Content-Type"), rec.buf.Bytes(), rec.size, rec.buf.Len() <= devBodyLimit)))
}
