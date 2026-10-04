// Package media delivers authorized original resources without transformations.
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Source is returned by a trusted repository after applying its ACL in SQL.
// RelativePath uses slash separators. Neither path belongs in an API response.
type Source struct {
	Root         string
	RelativePath string
	ContentType  string
	// ETag may contain a quoted strong content revision from the probe/index.
	// Leave empty when no reliable content revision has been calculated.
	ETag string
	// DeviceID is the device reported by the session's native login; empty
	// when none was reported. It only groups streams for device limits.
	DeviceID string
	// Limits are the user's delivery overrides; nil fields follow Options.Limits.
	Limits domain.DeliveryLimits
	// Charset is the detected charset of an external text subtitle. It is
	// only reported as a Content-Type parameter; the bytes are never
	// converted.
	Charset string
}

// Resolver must apply authorization in its lookup, check enabled users and
// sessions, and return ErrNotFound for missing or invisible sources alike.
type Resolver interface {
	Resolve(context.Context, access.Principal, string) (Source, error)
	// ResolveTrack binds one external subtitle or audio file of a source
	// (G10.9). It applies the same checks as Resolve to the source and also
	// returns ErrNotFound when the track does not belong to that source or is
	// of another kind. The handler derives the content type from the file
	// extension and ignores the returned ContentType. ETag follows the rules
	// of Source: leave it empty without a verified content revision.
	ResolveTrack(ctx context.Context, principal access.Principal, sourceID string, kind TrackKind, trackID string) (Source, error)
}

type Options struct {
	Budget        app.WorkBudget
	MaxConcurrent int
	LookupTimeout time.Duration
	WriteTimeout  time.Duration
	WriteError    func(http.ResponseWriter, *http.Request, error)
	// Sessions, when set, is polled every SessionCheckInterval (default 5s)
	// while a stream runs; a revoked session cuts the stream (G07.4).
	Sessions             SessionChecker
	SessionCheckInterval time.Duration
	// Limits are the concurrent playback and bandwidth limits (G07.4, G45.4).
	Limits Limits
	// Clock defaults to the system clock.
	Clock Clock
}

type Handler struct {
	resolver Resolver
	slots    chan struct{}
	options  Options
	buffers  sync.Pool
	clock    Clock
	limiter  *limiter
}

func NewHandler(resolver Resolver, options Options) (*Handler, error) {
	if resolver == nil || options.WriteError == nil || options.MaxConcurrent < 1 || options.WriteTimeout <= 0 || options.LookupTimeout < 0 {
		return nil, fmt.Errorf("direct delivery options: %w", ErrInvalidRequest)
	}
	if options.SessionCheckInterval < 0 {
		return nil, fmt.Errorf("direct delivery session check interval: %w", ErrInvalidRequest)
	}
	if err := options.Limits.validate(); err != nil {
		return nil, err
	}
	if options.LookupTimeout == 0 {
		options.LookupTimeout = 5 * time.Second
	}
	if options.SessionCheckInterval == 0 {
		options.SessionCheckInterval = DefaultSessionCheckInterval
	}
	clock := options.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Handler{resolver: resolver, slots: make(chan struct{}, options.MaxConcurrent), options: options, clock: clock, limiter: newLimiter(options.Limits, clock),
		buffers: sync.Pool{New: func() any { return new([32 << 10]byte) }},
	}, nil
}

// ServeSource accepts an opaque identifier already validated by the HTTP route.
// It deliberately accepts no request-supplied filesystem path or root.
func (h *Handler) ServeSource(w http.ResponseWriter, r *http.Request, sourceID string) {
	h.serve(w, r, sourceID, func(ctx context.Context, principal access.Principal) (Source, error) {
		return h.resolver.Resolve(ctx, principal, sourceID)
	})
}

// ServeTrack delivers one external subtitle or audio file of a source as it
// is (G10.9, G15.5, G16.4), through the same checks, limits, revocation and
// copy paths as ServeSource. A track counts as part of its source's playback:
// a session fetching the video and its sidecars holds one playback slot.
func (h *Handler) ServeTrack(w http.ResponseWriter, r *http.Request, sourceID string, kind TrackKind, trackID string) {
	if !kind.valid() || trackID == "" {
		// A malformed call is answered like a missing source, after the
		// authentication and session checks in serve.
		sourceID = ""
	}
	h.serve(w, r, sourceID, func(ctx context.Context, principal access.Principal) (Source, error) {
		source, err := h.resolver.ResolveTrack(ctx, principal, sourceID, kind, trackID)
		if err != nil {
			return Source{}, err
		}
		source.ContentType = TrackContentType(kind, path.Ext(source.RelativePath), source.Charset)
		return source, nil
	})
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, sourceID string, resolve func(context.Context, access.Principal) (Source, error)) {
	w.Header().Del("Content-Disposition")
	principal, authenticated := access.PrincipalFromContext(r.Context())
	if !authenticated {
		h.options.WriteError(w, r, ErrUnauthenticated)
		return
	}
	if principal.Kind != access.ClientNative {
		h.options.WriteError(w, r, ErrPlaybackDenied)
		return
	}
	if err := GuardProduction(r); err != nil {
		h.options.WriteError(w, r, err)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		h.options.WriteError(w, r, ErrMethodNotAllowed)
		return
	}
	if sourceID == "" {
		h.options.WriteError(w, r, ErrNotFound)
		return
	}
	if value := r.Header.Get("Range"); len(value) > 4096 || strings.Count(value, ",") >= 16 {
		h.options.WriteError(w, r, ErrInvalidRange)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		h.options.WriteError(w, r, ErrBusy)
		return
	}
	lookupCtx, cancelLookup := context.WithTimeout(r.Context(), h.options.LookupTimeout)
	source, err := resolve(lookupCtx, principal)
	lookupErr := lookupCtx.Err()
	cancelLookup()
	if errors.Is(lookupErr, context.DeadlineExceeded) && r.Context().Err() == nil {
		h.options.WriteError(w, r, ErrLookupTimeout)
		return
	}
	if err != nil {
		if r.Context().Err() == nil {
			h.options.WriteError(w, r, fmt.Errorf("resolve media: %w", err))
		}
		return
	}
	if r.Context().Err() != nil {
		return
	}
	// HEAD sends no media, so it is neither counted nor throttled.
	var admitted *admission
	if r.Method == http.MethodGet {
		if admitted, err = h.limiter.admit(principal, sourceID, source); err != nil {
			h.options.WriteError(w, r, err)
			return
		}
		defer admitted.release()
	}
	if h.options.Budget != nil {
		waitCtx, cancelWait := context.WithTimeout(r.Context(), h.options.LookupTimeout)
		release, budgetErr := h.options.Budget.Acquire(waitCtx, app.WorkIO)
		cancelWait()
		if budgetErr != nil {
			if r.Context().Err() != nil {
				return
			}
			if errors.Is(budgetErr, domain.ErrResourceBusy) || errors.Is(budgetErr, context.DeadlineExceeded) {
				w.Header().Set("Retry-After", "1")
				h.options.WriteError(w, r, ErrBusy)
			} else {
				h.options.WriteError(w, r, ErrIO)
			}
			return
		}
		defer release()
	}
	file, err := openSource(source)
	if err != nil {
		h.options.WriteError(w, r, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		h.options.WriteError(w, r, ErrNotFound)
		return
	}
	contentType := source.ContentType
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(source.RelativePath))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, _, err := mime.ParseMediaType(contentType); err != nil || strings.ContainsAny(contentType, "\r\n") {
		h.options.WriteError(w, r, ErrIO)
		return
	}
	if source.ETag != "" && !validETag(source.ETag) {
		h.options.WriteError(w, r, ErrIO)
		return
	}
	w.Header().Del("Content-Disposition")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	// Defense in depth (CodeQL reflected XSS): a source labelled text/html or
	// SVG must not run script in this origin if a client renders it. Error
	// responses written later restore the policy that was set before.
	errorPolicy := w.Header().Values("Content-Security-Policy")
	w.Header().Set("Content-Security-Policy", mediaContentSecurityPolicy)
	if source.ETag != "" {
		w.Header().Set("ETag", source.ETag)
	}
	controller := http.NewResponseController(w)
	// The stream context ends with the request or when the session watcher
	// finds the session revoked. Ending it closes the file and expires the
	// write deadline, so blocked reads, writes and throttle waits all stop.
	streamCtx, cancelStream := context.WithCancelCause(r.Context())
	defer cancelStream(nil)
	if h.options.Sessions != nil && r.Method == http.MethodGet {
		watcherDone := make(chan struct{})
		go h.watchSession(streamCtx, cancelStream, principal, watcherDone)
		defer func() {
			cancelStream(nil)
			<-watcherDone
		}()
	}
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(streamCtx, func() {
		defer close(callbackDone)
		_ = controller.SetWriteDeadline(time.Now())
		_ = file.Close()
	})
	defer func() {
		if !stop() {
			<-callbackDone
		}
		// A revoked stream keeps the expired deadline: the response is cut
		// short and the connection must not be reused or flushed further.
		if !errors.Is(context.Cause(streamCtx), ErrSessionRevoked) {
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}()
	// throttle is nil without a bandwidth limit. That is the only case in
	// which the zero-copy path (sendfile.go) may bypass the copy loop:
	// revocation does not depend on the write path, since ending streamCtx
	// closes the file and expires the write deadline.
	writer := &streamWriter{buffers: &h.buffers, ResponseWriter: w, request: r, ctx: streamCtx, controller: controller, timeout: h.options.WriteTimeout, writeError: h.options.WriteError, throttle: admitted.throttle(), errorPolicy: errorPolicy}
	reader := &contextFile{ctx: streamCtx, file: file}
	http.ServeContent(writer, r, "", info.ModTime(), reader)
}

// mediaContentSecurityPolicy sandboxes original media responses so content
// served with a document type cannot script the API origin.
const mediaContentSecurityPolicy = "sandbox; default-src 'none'"

func openSource(source Source) (*os.File, error) {
	if !filepath.IsAbs(source.Root) || !fs.ValidPath(source.RelativePath) || source.RelativePath == "." || strings.ContainsAny(source.RelativePath, "\\:\x00") {
		return nil, ErrNotFound
	}
	relative := filepath.Clean(filepath.FromSlash(source.RelativePath))
	if !filepath.IsLocal(relative) {
		return nil, ErrNotFound
	}
	root, err := os.OpenRoot(source.Root)
	if err != nil {
		return nil, ErrNotFound
	}
	defer root.Close()
	file, err := root.OpenFile(relative, readOnlyFlags(), 0)
	if err != nil {
		return nil, ErrNotFound
	}
	return file, nil
}

func validETag(value string) bool {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for _, char := range value[1 : len(value)-1] {
		if char < 0x21 || char == '"' || char > 0x7e {
			return false
		}
	}
	return true
}

type contextFile struct {
	ctx  context.Context
	file *os.File
}

func (f *contextFile) Read(data []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.file.Read(data)
}

func (f *contextFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.file.Seek(offset, whence)
}

type streamWriter struct {
	http.ResponseWriter
	request *http.Request
	// ctx is the stream context; nil means the request context.
	ctx        context.Context
	throttle   *throttle
	controller *http.ResponseController
	timeout    time.Duration
	writeError func(http.ResponseWriter, *http.Request, error)
	// errorPolicy is the Content-Security-Policy in force before the media
	// policy was set; error responses keep it.
	errorPolicy []string
	rejected    bool
	buffers     *sync.Pool
}

func (w *streamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *streamWriter) context() context.Context {
	if w.ctx != nil {
		return w.ctx
	}
	return w.request.Context()
}

func (w *streamWriter) WriteHeader(status int) {
	w.Header().Del("Content-Disposition")
	if status < 400 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.rejected = true
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Type")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Del("Content-Security-Policy")
	for _, value := range w.errorPolicy {
		w.Header().Add("Content-Security-Policy", value)
	}
	err := ErrIO
	switch status {
	case http.StatusRequestedRangeNotSatisfiable:
		err = ErrInvalidRange
	case http.StatusPreconditionFailed:
		err = ErrPreconditionFailed
	}
	w.writeError(w.ResponseWriter, w.request, err)
}

func (w *streamWriter) Write(data []byte) (int, error) {
	if w.rejected {
		return len(data), nil
	}
	if w.throttle == nil {
		return w.write(data)
	}
	written := 0
	for len(data) > 0 {
		piece := data[:min(len(data), throttleChunk)]
		if err := w.throttle.wait(w.context(), len(piece)); err != nil {
			return written, err
		}
		n, err := w.write(piece)
		written += n
		if err != nil {
			return written, err
		}
		data = data[n:]
	}
	return written, nil
}

func (w *streamWriter) write(data []byte) (int, error) {
	ctx := w.context()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	w.Header().Del("Content-Disposition")
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	// Cancellation may have expired the deadline just before the refresh above.
	// Recheck before writing so the refresh cannot undo an earlier cancellation.
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return w.ResponseWriter.Write(data)
}

func (w *streamWriter) ReadFrom(reader io.Reader) (int64, error) {
	if written, handled, err := w.zeroCopy(reader); handled {
		return written, err
	}
	// Hide optional copy interfaces to retain cancellation/deadline checks and a
	// predictable per-stream copy buffer, including multipart Range responses.
	buffer := w.buffers.Get().(*[32 << 10]byte)
	defer func() {
		// Release media bytes before another request can borrow the buffer.
		clear(buffer[:])
		w.buffers.Put(buffer)
	}()
	return io.CopyBuffer(struct{ io.Writer }{w}, struct{ io.Reader }{reader}, buffer[:])
}

var _ io.ReadSeeker = (*contextFile)(nil)
