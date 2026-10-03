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
}

// Resolver must apply authorization in its lookup, check enabled users and
// sessions, and return ErrNotFound for missing or invisible sources alike.
type Resolver interface {
	Resolve(context.Context, access.Principal, string) (Source, error)
}

type Options struct {
	Budget        app.WorkBudget
	MaxConcurrent int
	LookupTimeout time.Duration
	WriteTimeout  time.Duration
	WriteError    func(http.ResponseWriter, *http.Request, error)
}

type Handler struct {
	resolver Resolver
	slots    chan struct{}
	options  Options
	buffers  sync.Pool
}

func NewHandler(resolver Resolver, options Options) (*Handler, error) {
	if resolver == nil || options.WriteError == nil || options.MaxConcurrent < 1 || options.WriteTimeout <= 0 || options.LookupTimeout < 0 {
		return nil, fmt.Errorf("direct delivery options: %w", ErrInvalidRequest)
	}
	if options.LookupTimeout == 0 {
		options.LookupTimeout = 5 * time.Second
	}
	return &Handler{resolver: resolver, slots: make(chan struct{}, options.MaxConcurrent), options: options,
		buffers: sync.Pool{New: func() any { return new([32 << 10]byte) }},
	}, nil
}

// ServeSource accepts an opaque identifier already validated by the HTTP route.
// It deliberately accepts no request-supplied filesystem path or root.
func (h *Handler) ServeSource(w http.ResponseWriter, r *http.Request, sourceID string) {
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
	source, err := h.resolver.Resolve(lookupCtx, principal, sourceID)
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
	if source.ETag != "" {
		w.Header().Set("ETag", source.ETag)
	}
	controller := http.NewResponseController(w)
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(r.Context(), func() {
		defer close(callbackDone)
		_ = controller.SetWriteDeadline(time.Now())
		_ = file.Close()
	})
	defer func() {
		if !stop() {
			<-callbackDone
		}
		_ = controller.SetWriteDeadline(time.Time{})
	}()
	writer := &streamWriter{buffers: &h.buffers, ResponseWriter: w, request: r, controller: controller, timeout: h.options.WriteTimeout, writeError: h.options.WriteError}
	reader := &contextFile{ctx: r.Context(), file: file}
	http.ServeContent(writer, r, "", info.ModTime(), reader)
}

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
	request    *http.Request
	controller *http.ResponseController
	timeout    time.Duration
	writeError func(http.ResponseWriter, *http.Request, error)
	rejected   bool
	buffers    *sync.Pool
}

func (w *streamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

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
	if err := w.request.Context().Err(); err != nil {
		return 0, err
	}
	w.Header().Del("Content-Disposition")
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	// Cancellation may have expired the deadline just before the refresh above.
	// Recheck before writing so the refresh cannot undo an earlier cancellation.
	if err := w.request.Context().Err(); err != nil {
		return 0, err
	}
	return w.ResponseWriter.Write(data)
}

func (w *streamWriter) ReadFrom(reader io.Reader) (int64, error) {
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
